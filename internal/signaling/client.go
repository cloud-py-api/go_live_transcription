// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package signaling

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hraban/opus"
	"github.com/nextcloud/go_live_transcription/internal/appapi"
	"github.com/nextcloud/go_live_transcription/internal/constants"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

var (
	ErrRateLimited = errors.New("rate limited by HPB")
	ErrDefunct     = errors.New("spreed client is defunct")
)

type SpreedClient struct {
	mu sync.Mutex

	roomToken   string
	roomLangID  string
	secret      string
	wsURL       string
	backendURL  string
	hpbSettings *HPBSettings

	conn      *websocket.Conn
	msgID     atomic.Int64
	sessionID string
	resumeID  string
	defunct   atomic.Bool

	peerConns   map[string]*webrtc.PeerConnection
	peerConnsMu sync.Mutex

	targets        map[string]struct{} // HPB session IDs receiving transcripts
	ncSidMap       map[string]string   // NC session ID → HPB session ID
	ncSidWaitStash map[string]struct{} // deferred targets awaiting ID mapping
	targetMu       sync.Mutex

	TranscriptCh chan Transcript
	PCMAudioCh   chan PCMAudio

	deferredCloseTimer *time.Timer
	cancel             context.CancelFunc
	leaveCallCb        func(roomToken string)

	logger *slog.Logger
}

type Transcript struct {
	Final            bool
	LangID           string
	Message          string
	SpeakerSessionID string
}

type PCMAudio struct {
	SessionID  string
	Samples    []int16
	SampleRate int
}

func NewSpreedClient(
	roomToken string,
	hpbSettings *HPBSettings,
	roomLangID string,
	cfg *appapi.Config,
	leaveCallCb func(string),
) *SpreedClient {
	wsURL := sanitizeWebSocketURL(cfg.HPBUrl)
	backendURL := cfg.NextcloudURL + "/ocs/v2.php/apps/spreed/api/v3/signaling/backend"

	return &SpreedClient{
		roomToken:      roomToken,
		roomLangID:     roomLangID,
		secret:         cfg.InternalSecret,
		wsURL:          wsURL,
		backendURL:     backendURL,
		hpbSettings:    hpbSettings,
		peerConns:      make(map[string]*webrtc.PeerConnection),
		targets:        make(map[string]struct{}),
		ncSidMap:       make(map[string]string),
		ncSidWaitStash: make(map[string]struct{}),
		TranscriptCh:   make(chan Transcript, 1000),
		PCMAudioCh:     make(chan PCMAudio, 100),
		leaveCallCb:    leaveCallCb,
		logger:         slog.With("room_token", roomToken),
	}
}

func (sc *SpreedClient) Connect(ctx context.Context, reconnect ReconnectMethod) (SigConnectResult, error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if sc.conn != nil && reconnect != FullReconnect {
		sc.logger.Debug("already connected, skipping")
		return SigConnectSuccess, nil
	}

	if reconnect == FullReconnect {
		sc.logger.Info("performing full reconnect")
		sc.closeInternal()
		sc.resumeID = ""
		sc.sessionID = ""
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 30 * time.Second,
	}

	parsedURL, _ := url.Parse(sc.wsURL)
	if parsedURL != nil && parsedURL.Scheme == "wss" {
		skipCert := os.Getenv("SKIP_CERT_VERIFY")
		if skipCert == "true" || skipCert == "1" {
			dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
	}

	conn, _, err := dialer.DialContext(ctx, sc.wsURL, nil)
	if err != nil {
		sc.logger.Error("failed to connect to HPB", "error", err)
		return SigConnectRetry, fmt.Errorf("websocket dial: %w", err)
	}
	sc.conn = conn

	if reconnect == ShortResume && sc.resumeID != "" {
		ok, err := sc.resumeConnection(ctx)
		if err != nil {
			if errors.Is(err, ErrRateLimited) {
				return SigConnectFailure, err
			}
			sc.logger.Warn("short resume failed, will full reconnect", "error", err)
			return SigConnectRetry, nil
		}
		if ok {
			sc.logger.Info("resumed connection")
			sc.defunct.Store(false)
			sc.sendInCall()
			sc.sendJoin()
			return SigConnectSuccess, nil
		}
		// resume failed, need full reconnect
		return SigConnectRetry, nil
	}

	if err := sc.sendHello(); err != nil {
		sc.logger.Error("failed to send hello", "error", err)
		return SigConnectFailure, err
	}

	for i := 0; i < 10; i++ {
		msg, err := sc.receiveMessage(constants.MsgReceiveTimeout)
		if err != nil {
			sc.logger.Error("no message during handshake", "error", err)
			return SigConnectFailure, err
		}

		switch msg.Type {
		case "error":
			code := ""
			if msg.Error != nil {
				code = msg.Error.Code
			}
			sc.logger.Error("signaling error during connect", "code", code)
			if code == "duplicate_session" {
				return SigConnectFailure, fmt.Errorf("duplicate session")
			}
			if code == "room_join_failed" {
				return SigConnectRetry, fmt.Errorf("room join failed")
			}
			return SigConnectFailure, fmt.Errorf("signaling error: %s", code)

		case "bye":
			sc.logger.Info("received bye during connect")
			return SigConnectFailure, fmt.Errorf("received bye")

		case "welcome":
			sc.logger.Debug("received welcome")
			continue

		case "hello":
			if msg.Hello != nil {
				sc.sessionID = msg.Hello.SessionID
				sc.resumeID = msg.Hello.ResumeID
				sc.logger.Info("hello handshake complete",
					"session_id", sc.sessionID,
					"resume_id", sc.resumeID,
				)
			}
			goto connected
		}
	}
	return SigConnectFailure, fmt.Errorf("did not receive hello response")

connected:
	sc.defunct.Store(false)

	monCtx, monCancel := context.WithCancel(ctx)
	sc.cancel = monCancel
	go sc.monitor(monCtx)

	sc.sendInCall()
	sc.sendJoin()

	sc.targetMu.Lock()
	if len(sc.targets) == 0 {
		sc.startDeferredClose()
	}
	sc.targetMu.Unlock()

	sc.logger.Info("connected to signaling server")
	return SigConnectSuccess, nil
}

func (sc *SpreedClient) IsDefunct() bool {
	return sc.defunct.Load()
}

func (sc *SpreedClient) SetRoomLangID(langID string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.roomLangID = langID
}

func (sc *SpreedClient) RoomLangID() string {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.roomLangID
}

func (sc *SpreedClient) Close() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.closeInternal()
}

func (sc *SpreedClient) closeInternal() {
	if sc.defunct.Load() {
		return
	}

	if sc.cancel != nil {
		sc.cancel()
		sc.cancel = nil
	}

	sc.targetMu.Lock()
	sc.cancelDeferredClose()
	sc.targetMu.Unlock()

	if sc.conn != nil {
		sc.sendMessageLocked(SignalingMessage{Type: "bye", Bye: &ByeMessage{}})
	}

	sc.peerConnsMu.Lock()
	for sid, pc := range sc.peerConns {
		pc.Close()
		delete(sc.peerConns, sid)
	}
	sc.peerConnsMu.Unlock()

	if sc.conn != nil {
		sc.conn.Close()
		sc.conn = nil
	}

	sc.defunct.Store(true)
	sc.logger.Info("client closed")

	if sc.leaveCallCb != nil {
		go sc.leaveCallCb(sc.roomToken)
	}
}

func (sc *SpreedClient) AddTarget(ncSessionID string) {
	sc.targetMu.Lock()
	defer sc.targetMu.Unlock()

	sc.cancelDeferredClose()

	hpbSid, ok := sc.ncSidMap[ncSessionID]
	if !ok {
		sc.ncSidWaitStash[ncSessionID] = struct{}{}
		sc.logger.Debug("HPB session ID not found, deferring target add", "nc_session_id", ncSessionID)
		return
	}

	delete(sc.ncSidWaitStash, ncSessionID)
	sc.targets[hpbSid] = struct{}{}
	sc.logger.Debug("added target", "session_id", hpbSid, "nc_session_id", ncSessionID)
}

func (sc *SpreedClient) RemoveTarget(ncSessionID string) {
	sc.targetMu.Lock()
	defer sc.targetMu.Unlock()

	delete(sc.ncSidWaitStash, ncSessionID)

	hpbSid, ok := sc.ncSidMap[ncSessionID]
	if !ok {
		return
	}
	delete(sc.targets, hpbSid)
	sc.logger.Debug("removed target", "session_id", hpbSid, "nc_session_id", ncSessionID)

	if len(sc.targets) == 0 {
		sc.startDeferredClose()
	}
}

func (sc *SpreedClient) removeTargetByHPBSid(sessionID string) {
	sc.targetMu.Lock()
	defer sc.targetMu.Unlock()
	delete(sc.targets, sessionID)

	if len(sc.targets) == 0 {
		sc.startDeferredClose()
	}
}

// Must be called with targetMu held.
func (sc *SpreedClient) startDeferredClose() {
	sc.cancelDeferredClose()
	sc.logger.Debug("starting deferred close timer", "timeout", constants.CallLeaveTimeout)
	sc.deferredCloseTimer = time.AfterFunc(constants.CallLeaveTimeout, func() {
		if sc.defunct.Load() {
			return
		}
		sc.targetMu.Lock()
		noTargets := len(sc.targets) == 0
		sc.targetMu.Unlock()

		if noTargets {
			sc.logger.Info("no targets after deferred close timeout, leaving call")
			sc.Close()
		}
	})
}

// Must be called with targetMu held.
func (sc *SpreedClient) cancelDeferredClose() {
	if sc.deferredCloseTimer != nil {
		sc.deferredCloseTimer.Stop()
		sc.deferredCloseTimer = nil
	}
}

func (sc *SpreedClient) monitor(ctx context.Context) {
	sc.logger.Debug("signaling monitor started")
	defer sc.logger.Debug("signaling monitor stopped")

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msg, err := sc.receiveMessage(0)
		if err != nil {
			if ctx.Err() != nil {
				return // context cancelled
			}
			sc.logger.Error("websocket error in monitor, closing", "error", err)
			sc.Close()
			return
		}

		switch msg.Type {
		case "error":
			code := ""
			if msg.Error != nil {
				code = msg.Error.Code
			}
			sc.logger.Error("signaling error", "code", code)
			if code == "processing_failed" {
				continue // recoverable
			}
			sc.Close()
			return

		case "event":
			sc.handleEvent(msg)

		case "message":
			sc.handleMessage(ctx, msg)

		case "bye":
			sc.logger.Info("received bye, closing")
			sc.Close()
			return
		}
	}
}

func (sc *SpreedClient) handleEvent(msg *SignalingMessage) {
	if msg.Event == nil || msg.Event.Target != "participants" || msg.Event.Type != "update" {
		return
	}
	if msg.Event.Update == nil {
		return
	}

	if msg.Event.Update.All && msg.Event.Update.InCall == CallFlagDisconnected {
		sc.logger.Info("call ended for everyone")
		sc.Close()
		return
	}

	for _, user := range msg.Event.Update.Users {
		if user.Internal {
			continue
		}

		if user.InCall == CallFlagDisconnected {
			sc.logger.Debug("user disconnected", "session_id", user.SessionID)
			sc.removeTargetByHPBSid(user.SessionID)

			sc.peerConnsMu.Lock()
			if pc, ok := sc.peerConns[user.SessionID]; ok {
				pc.Close()
				delete(sc.peerConns, user.SessionID)
			}
			sc.peerConnsMu.Unlock()

			sc.targetMu.Lock()
			if user.NextcloudSessionID != "" {
				delete(sc.ncSidMap, user.NextcloudSessionID)
			}
			sc.targetMu.Unlock()
			continue
		}

		if user.NextcloudSessionID != "" {
			sc.targetMu.Lock()
			sc.ncSidMap[user.NextcloudSessionID] = user.SessionID

			if _, waiting := sc.ncSidWaitStash[user.NextcloudSessionID]; waiting {
				delete(sc.ncSidWaitStash, user.NextcloudSessionID)
				sc.targets[user.SessionID] = struct{}{}
				sc.logger.Debug("resolved deferred target",
					"nc_session_id", user.NextcloudSessionID,
					"session_id", user.SessionID,
				)
			}
			sc.targetMu.Unlock()
		}

		if user.InCall&CallFlagInCall != 0 && user.InCall&CallFlagWithAudio != 0 {
			sc.peerConnsMu.Lock()
			_, exists := sc.peerConns[user.SessionID]
			sc.peerConnsMu.Unlock()

			if !exists {
				sc.logger.Debug("user joined with audio, requesting offer", "session_id", user.SessionID)
				sc.sendOfferRequest(user.SessionID)
			}
		}
	}

	if len(msg.Event.Update.Users) == 2 {
		sc.checkLastUserLeft(msg.Event.Update.Users)
	}
}

func (sc *SpreedClient) checkLastUserLeft(users []UserUpdateEntry) {
	var us, them *UserUpdateEntry
	for i := range users {
		if users[i].SessionID == sc.sessionID {
			us = &users[i]
		} else {
			them = &users[i]
		}
	}
	if us == nil || them == nil {
		return
	}
	if us.InCall&CallFlagInCall != 0 && them.InCall == CallFlagDisconnected {
		sc.logger.Info("last user left the call, closing")
		sc.Close()
	}
}

func (sc *SpreedClient) handleMessage(ctx context.Context, msg *SignalingMessage) {
	if msg.Message == nil || msg.Message.Data == nil {
		return
	}

	switch msg.Message.Data.Type {
	case "offer":
		sc.handleOffer(ctx, msg)
	case "candidate":
		sc.handleCandidate(msg)
	}
}

func (sc *SpreedClient) handleOffer(ctx context.Context, msg *SignalingMessage) {
	if msg.Message.Sender == nil || msg.Message.Data.Payload == nil {
		return
	}

	spkrSid := msg.Message.Sender.SessionID
	offerSid := msg.Message.Data.SID
	sdp := msg.Message.Data.Payload.SDP

	sc.logger.Debug("received offer", "speaker_sid", spkrSid, "offer_sid", offerSid)

	sc.peerConnsMu.Lock()
	if oldPC, ok := sc.peerConns[spkrSid]; ok {
		oldPC.Close()
		delete(sc.peerConns, spkrSid)
	}
	sc.peerConnsMu.Unlock()

	var iceServers []webrtc.ICEServer
	for _, stun := range sc.hpbSettings.StunServers {
		iceServers = append(iceServers, webrtc.ICEServer{URLs: stun.URLs})
	}
	for _, turn := range sc.hpbSettings.TurnServers {
		iceServers = append(iceServers, webrtc.ICEServer{
			URLs:       turn.URLs,
			Username:   turn.Username,
			Credential: turn.Credential,
		})
	}

	config := webrtc.Configuration{ICEServers: iceServers}
	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		sc.logger.Error("failed to create peer connection", "error", err)
		return
	}

	_, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	if err != nil {
		sc.logger.Error("failed to add audio transceiver", "error", err)
		pc.Close()
		return
	}

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		sc.logger.Debug("peer connection state changed",
			"session_id", spkrSid, "state", state.String())
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			sc.peerConnsMu.Lock()
			delete(sc.peerConns, spkrSid)
			sc.peerConnsMu.Unlock()
		}
	})

	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if track.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		sc.logger.Debug("receiving audio track", "session_id", spkrSid,
			"codec", track.Codec().MimeType)
		go sc.readAudioTrack(ctx, spkrSid, track)
	})

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		candidateStr := c.ToJSON().Candidate
		sc.sendCandidate(spkrSid, offerSid, candidateStr)
	})

	err = pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdp,
	})
	if err != nil {
		sc.logger.Error("failed to set remote description", "error", err)
		pc.Close()
		return
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		sc.logger.Error("failed to create answer", "error", err)
		pc.Close()
		return
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		sc.logger.Error("failed to set local description", "error", err)
		pc.Close()
		return
	}

	sc.peerConnsMu.Lock()
	sc.peerConns[spkrSid] = pc
	sc.peerConnsMu.Unlock()

	fromSid := spkrSid
	if msg.Message.Data.From != "" {
		fromSid = msg.Message.Data.From
	}
	sc.sendOfferAnswer(fromSid, offerSid, answer.SDP)

	sc.logger.Debug("sent answer for offer", "speaker_sid", spkrSid)
}

func (sc *SpreedClient) handleCandidate(msg *SignalingMessage) {
	if msg.Message.Sender == nil || msg.Message.Data.Payload == nil || msg.Message.Data.Payload.Candidate == nil {
		return
	}

	senderSid := msg.Message.Sender.SessionID
	candidate := msg.Message.Data.Payload.Candidate

	sc.peerConnsMu.Lock()
	pc, ok := sc.peerConns[senderSid]
	sc.peerConnsMu.Unlock()

	if !ok {
		return
	}

	iceCandidate := webrtc.ICECandidateInit{
		Candidate:     candidate.Candidate,
		SDPMid:        &candidate.SDPMid,
		SDPMLineIndex: uint16Ptr(uint16(candidate.SDPMLineIndex)),
	}

	if err := pc.AddICECandidate(iceCandidate); err != nil {
		sc.logger.Warn("failed to add ICE candidate", "error", err, "session_id", senderSid)
	}
}

func (sc *SpreedClient) readAudioTrack(ctx context.Context, sessionID string, track *webrtc.TrackRemote) {
	sc.logger.Info("audio track reader started", "session_id", sessionID,
		"codec", track.Codec().MimeType,
		"sample_rate", track.Codec().ClockRate,
		"channels", track.Codec().Channels,
	)
	defer sc.logger.Info("audio track reader stopped", "session_id", sessionID)

	const sampleRate = 48000
	const channels = 1
	dec, err := opus.NewDecoder(sampleRate, channels)
	if err != nil {
		sc.logger.Error("failed to create opus decoder", "error", err, "session_id", sessionID)
		return
	}

	pcmBuf := make([]int16, 5760) // max 120ms at 48kHz

	rtpBuf := make([]byte, 4096)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, _, readErr := track.Read(rtpBuf)
		if readErr != nil {
			if ctx.Err() != nil {
				return
			}
			sc.logger.Debug("track read error", "session_id", sessionID, "error", readErr)
			return
		}
		if n == 0 {
			continue
		}

		packet := &rtp.Packet{}
		if err := packet.Unmarshal(rtpBuf[:n]); err != nil {
			continue
		}
		if len(packet.Payload) == 0 {
			continue
		}

		samplesDecoded, err := dec.Decode(packet.Payload, pcmBuf)
		if err != nil {
			sc.logger.Debug("opus decode error", "error", err, "session_id", sessionID)
			continue
		}
		if samplesDecoded == 0 {
			continue
		}

		samples := make([]int16, samplesDecoded)
		copy(samples, pcmBuf[:samplesDecoded])

		select {
		case sc.PCMAudioCh <- PCMAudio{
			SessionID:  sessionID,
			Samples:    samples,
			SampleRate: sampleRate,
		}:
		default:
		}
	}
}

func (sc *SpreedClient) SendMessage(msg SignalingMessage) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.sendMessageLocked(msg)
}

func (sc *SpreedClient) sendMessageLocked(msg SignalingMessage) {
	if sc.conn == nil {
		return
	}
	id := sc.msgID.Add(1)
	msg.ID = strconv.FormatInt(id, 10)

	data, err := json.Marshal(msg)
	if err != nil {
		sc.logger.Error("failed to marshal message", "error", err)
		return
	}

	if err := sc.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		sc.logger.Error("failed to send message", "error", err)
	}
}

func (sc *SpreedClient) receiveMessage(timeout time.Duration) (*SignalingMessage, error) {
	if sc.conn == nil {
		return nil, fmt.Errorf("no connection")
	}

	if timeout > 0 {
		sc.conn.SetReadDeadline(time.Now().Add(timeout))
		defer sc.conn.SetReadDeadline(time.Time{})
	}

	_, data, err := sc.conn.ReadMessage()
	if err != nil {
		return nil, err
	}

	var msg SignalingMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}

	return &msg, nil
}

func (sc *SpreedClient) resumeConnection(ctx context.Context) (bool, error) {
	sc.sendMessageLocked(SignalingMessage{
		Type: "hello",
		Hello: &HelloMessage{
			Version:  "2.0",
			ResumeID: sc.resumeID,
		},
	})

	for i := 0; i < 10; i++ {
		msg, err := sc.receiveMessage(constants.MsgReceiveTimeout)
		if err != nil {
			return false, err
		}

		if msg.Type == "hello" && msg.Hello != nil {
			sc.sessionID = msg.Hello.SessionID
			return true, nil
		}

		if msg.Type == "error" {
			code := ""
			if msg.Error != nil {
				code = msg.Error.Code
			}
			if code == "no_such_session" {
				return false, nil // need full reconnect
			}
			if code == "too_many_requests" {
				return false, ErrRateLimited
			}
			return false, nil
		}
	}

	return false, nil
}

func (sc *SpreedClient) sendHello() error {
	nonce := generateNonce()
	token := hmacSHA256(sc.secret, nonce)

	sc.sendMessageLocked(SignalingMessage{
		Type: "hello",
		Hello: &HelloMessage{
			Version: "2.0",
			Auth: &HelloAuth{
				Type: "internal",
				Params: &HelloAuthParams{
					Random:  nonce,
					Token:   token,
					Backend: sc.backendURL,
				},
			},
		},
	})
	return nil
}

func (sc *SpreedClient) sendInCall() {
	sc.sendMessageLocked(SignalingMessage{
		Type: "internal",
		Internal: &InternalMessage{
			Type:   "incall",
			InCall: &InCallMessage{InCall: CallFlagInCall},
		},
	})
}

func (sc *SpreedClient) sendJoin() {
	sc.sendMessageLocked(SignalingMessage{
		Type: "room",
		Room: &RoomMessage{
			RoomID:    sc.roomToken,
			SessionID: sc.sessionID,
		},
	})
}

func (sc *SpreedClient) sendOfferRequest(publisherSessionID string) {
	sc.SendMessage(SignalingMessage{
		Type: "message",
		Message: &DataMessage{
			Recipient: &Recipient{Type: "session", SessionID: publisherSessionID},
			Data: &MessagePayload{
				Type:     "requestoffer",
				RoomType: "video",
			},
		},
	})
}

func (sc *SpreedClient) sendOfferAnswer(publisherSessionID, offerSid, sdp string) {
	sc.SendMessage(SignalingMessage{
		Type: "message",
		Message: &DataMessage{
			Recipient: &Recipient{Type: "session", SessionID: publisherSessionID},
			Data: &MessagePayload{
				To:       publisherSessionID,
				Type:     "answer",
				RoomType: "video",
				SID:      offerSid,
				Payload: &SDPPayload{
					Nick: "live_transcription",
					Type: "answer",
					SDP:  sdp,
				},
			},
		},
	})
}

func (sc *SpreedClient) sendCandidate(sender, offerSid, candidateStr string) {
	sc.SendMessage(SignalingMessage{
		Type: "message",
		Message: &DataMessage{
			Recipient: &Recipient{Type: "session", SessionID: sender},
			Data: &MessagePayload{
				To:       sender,
				Type:     "candidate",
				SID:      offerSid,
				RoomType: "video",
				Payload: &SDPPayload{
					Candidate: &CandidateInfo{
						Candidate:     candidateStr,
						SDPMLineIndex: 0,
						SDPMid:        "0",
					},
				},
			},
		},
	})
}

// SendTranscript sends a transcript to all targets. If excludeNcSid is
// non-nil, targets whose Nextcloud session ID satisfies it are skipped
// (used to suppress original-language finals for translation recipients).
func (sc *SpreedClient) SendTranscript(t Transcript, excludeNcSid func(string) bool) {
	sc.targetMu.Lock()
	type target struct {
		hpbSid string
		ncSid  string
	}
	targets := make([]target, 0, len(sc.targets))
	// Build reverse map only when we need to exclude
	var hpbToNc map[string]string
	if excludeNcSid != nil {
		hpbToNc = make(map[string]string, len(sc.ncSidMap))
		for nc, hpb := range sc.ncSidMap {
			hpbToNc[hpb] = nc
		}
	}
	for sid := range sc.targets {
		nc := ""
		if hpbToNc != nil {
			nc = hpbToNc[sid]
		}
		targets = append(targets, target{hpbSid: sid, ncSid: nc})
	}
	sc.targetMu.Unlock()

	if len(targets) == 0 {
		return
	}

	finalVal := t.Final
	for _, tgt := range targets {
		if excludeNcSid != nil && tgt.ncSid != "" && excludeNcSid(tgt.ncSid) {
			continue
		}
		sc.SendMessage(SignalingMessage{
			Type: "message",
			Message: &DataMessage{
				Recipient: &Recipient{Type: "session", SessionID: tgt.hpbSid},
				Data: &MessagePayload{
					Final:            &finalVal,
					LangID:           t.LangID,
					Message:          t.Message,
					SpeakerSessionID: t.SpeakerSessionID,
					Type:             "transcript",
				},
			},
		})
	}
}

// ResolveNcSessionID maps a Nextcloud session ID to the corresponding HPB session ID.
// Returns empty string if not found.
func (sc *SpreedClient) ResolveNcSessionID(ncSessionID string) string {
	sc.targetMu.Lock()
	defer sc.targetMu.Unlock()
	return sc.ncSidMap[ncSessionID]
}

func hmacSHA256(key, message string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func generateNonce() string {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		// Fallback to less random source
		for i := range b {
			b[i] = byte(time.Now().UnixNano() & 0xFF)
		}
	}
	return hex.EncodeToString(b)
}

var httpToWS = regexp.MustCompile(`^http://`)
var httpsToWSS = regexp.MustCompile(`^https://`)

func sanitizeWebSocketURL(wsURL string) string {
	wsURL = httpToWS.ReplaceAllString(wsURL, "ws://")
	wsURL = httpsToWSS.ReplaceAllString(wsURL, "wss://")
	wsURL = strings.TrimRight(wsURL, "/")
	if !strings.HasSuffix(wsURL, "/spreed") {
		wsURL += "/spreed"
	}
	return wsURL
}

func uint16Ptr(v uint16) *uint16 {
	return &v
}
