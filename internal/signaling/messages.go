// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package signaling

type HPBSettings struct {
	Server      string       `json:"server"`
	StunServers []StunServer `json:"stunservers"`
	TurnServers []TurnServer `json:"turnservers"`
}

type StunServer struct {
	URLs []string `json:"urls"`
}

type TurnServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
}

type SigConnectResult int

const (
	SigConnectSuccess SigConnectResult = 0
	SigConnectFailure SigConnectResult = 1 // do not retry
	SigConnectRetry   SigConnectResult = 2
)

type ReconnectMethod int

const (
	NoReconnect   ReconnectMethod = 0
	ShortResume   ReconnectMethod = 1
	FullReconnect ReconnectMethod = 2
)

type CallFlag int

const (
	CallFlagDisconnected CallFlag = 0
	CallFlagInCall       CallFlag = 1
	CallFlagWithAudio    CallFlag = 2
	CallFlagWithVideo    CallFlag = 4
	CallFlagWithPhone    CallFlag = 8
)

type SignalingMessage struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type"`

	Hello    *HelloMessage    `json:"hello,omitempty"`
	Room     *RoomMessage     `json:"room,omitempty"`
	Message  *DataMessage     `json:"message,omitempty"`
	Internal *InternalMessage `json:"internal,omitempty"`
	Event    *EventMessage    `json:"event,omitempty"`
	Error    *ErrorMessage    `json:"error,omitempty"`
	Bye      *ByeMessage      `json:"bye,omitempty"`
}

type HelloMessage struct {
	Version   string     `json:"version,omitempty"`
	ResumeID  string     `json:"resumeid,omitempty"`
	SessionID string     `json:"sessionid,omitempty"`
	Auth      *HelloAuth `json:"auth,omitempty"`
}

type HelloAuth struct {
	Type   string           `json:"type"`
	Params *HelloAuthParams `json:"params,omitempty"`
}

type HelloAuthParams struct {
	Random  string `json:"random"`
	Token   string `json:"token"`
	Backend string `json:"backend"`
}

type RoomMessage struct {
	RoomID    string `json:"roomid"`
	SessionID string `json:"sessionid,omitempty"`
}

type DataMessage struct {
	Recipient *Recipient      `json:"recipient,omitempty"`
	Sender    *Sender         `json:"sender,omitempty"`
	Data      *MessagePayload `json:"data,omitempty"`
}

type Recipient struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionid"`
}

type Sender struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionid"`
}

type MessagePayload struct {
	Type     string      `json:"type"`
	RoomType string      `json:"roomType,omitempty"`
	To       string      `json:"to,omitempty"`
	From     string      `json:"from,omitempty"`
	SID      string      `json:"sid,omitempty"`
	Payload  *SDPPayload `json:"payload,omitempty"`

	Final            *bool  `json:"final,omitempty"`
	LangID           string `json:"langId,omitempty"`
	Message          string `json:"message,omitempty"`
	SpeakerSessionID string `json:"speakerSessionId,omitempty"`
}

type SDPPayload struct {
	Nick      string         `json:"nick,omitempty"`
	Type      string         `json:"type"`
	SDP       string         `json:"sdp,omitempty"`
	Candidate *CandidateInfo `json:"candidate,omitempty"`
}

type CandidateInfo struct {
	Candidate     string `json:"candidate"`
	SDPMLineIndex int    `json:"sdpMLineIndex"`
	SDPMid        string `json:"sdpMid"`
}

type InternalMessage struct {
	Type   string         `json:"type"`
	InCall *InCallMessage `json:"incall,omitempty"`
}

type InCallMessage struct {
	InCall CallFlag `json:"incall"`
}

type EventMessage struct {
	Target string       `json:"target"`
	Type   string       `json:"type"`
	Update *EventUpdate `json:"update,omitempty"`
}

type EventUpdate struct {
	All    bool              `json:"all,omitempty"`
	InCall CallFlag          `json:"incall,omitempty"`
	Users  []UserUpdateEntry `json:"users,omitempty"`
}

type UserUpdateEntry struct {
	SessionID          string   `json:"sessionId"`
	NextcloudSessionID string   `json:"nextcloudSessionId,omitempty"`
	InCall             CallFlag `json:"inCall"`
	Internal           bool     `json:"internal,omitempty"`
}

type ErrorMessage struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
	Details string `json:"details,omitempty"`
}

type ByeMessage struct{}
