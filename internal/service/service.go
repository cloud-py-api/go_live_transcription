// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nextcloud/go_live_transcription/internal/appapi"
	"github.com/nextcloud/go_live_transcription/internal/constants"
	"github.com/nextcloud/go_live_transcription/internal/signaling"
	"github.com/nextcloud/go_live_transcription/internal/transcript"
	"github.com/nextcloud/go_live_transcription/internal/translation"
	"github.com/nextcloud/go_live_transcription/internal/vosk"
)

type roomState struct {
	client      *signaling.SpreedClient
	sender      *transcript.Sender
	audioWorker *vosk.AudioWorker
	meta        *translation.MetaTranslator
	transSender *translation.TranslatedSender
	cancel      context.CancelFunc
}

type Application struct {
	mu          sync.Mutex
	cfg         *appapi.Config
	client      *appapi.Client
	hpbSettings *signaling.HPBSettings
	rooms       map[string]*roomState
}

func NewApplication(cfg *appapi.Config, client *appapi.Client) *Application {
	app := &Application{
		cfg:    cfg,
		client: client,
		rooms:  make(map[string]*roomState),
	}

	if cfg.HPBUrl != "" && cfg.InternalSecret != "" {
		hpbSettings, err := app.fetchHPBSettings()
		if err != nil {
			slog.Warn("failed to fetch HPB settings on startup, will retry on first call", "error", err)
		} else {
			app.hpbSettings = hpbSettings
		}
	} else {
		slog.Info("HPB not configured (LT_HPB_URL/LT_INTERNAL_SECRET not set)")
	}

	slog.Info("application service initialized")
	return app
}

func (app *Application) fetchHPBSettings() (*signaling.HPBSettings, error) {
	data, err := app.client.OCSGet("/ocs/v2.php/apps/spreed/api/v3/signaling/settings", "admin")
	if err != nil {
		return nil, fmt.Errorf("fetching signaling settings: %w", err)
	}

	var settings signaling.HPBSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parsing signaling settings: %w", err)
	}

	slog.Info("HPB settings retrieved",
		"server", settings.Server,
		"stun_count", len(settings.StunServers),
		"turn_count", len(settings.TurnServers),
	)
	return &settings, nil
}

func (app *Application) TranscriptReq(ctx context.Context, roomToken, ncSessionID, langID string, enable bool) error {
	app.mu.Lock()

	if rs, ok := app.rooms[roomToken]; ok {
		if rs.client.IsDefunct() {
			if enable {
				// Client is defunct, recreate after delay
				app.mu.Unlock()
				slog.Info("client defunct, deferring restart", "room_token", roomToken)
				time.Sleep(5 * time.Second)
				return app.TranscriptReq(ctx, roomToken, ncSessionID, langID, enable)
			}
			app.mu.Unlock()
			return nil
		}

		if enable {
			rs.client.AddTarget(ncSessionID)
		} else {
			rs.client.RemoveTarget(ncSessionID)
		}
		app.mu.Unlock()
		return nil
	}
	app.mu.Unlock()

	if !enable {
		slog.Info("no active call, ignoring disable request", "room_token", roomToken)
		return nil
	}

	// New call — ensure HPB settings
	if app.hpbSettings == nil {
		settings, err := app.fetchHPBSettings()
		if err != nil {
			return fmt.Errorf("HPB settings unavailable: %w", err)
		}
		app.hpbSettings = settings
	}

	client := signaling.NewSpreedClient(
		roomToken,
		app.hpbSettings,
		langID,
		app.cfg,
		app.leaveCallCb,
	)

	sender := transcript.NewSender(client, client.TranscriptCh)
	transcriberMgr := vosk.NewTranscriberManager(langID, 16000, client.TranscriptCh)
	audioWorker := vosk.NewAudioWorker(client, transcriberMgr)

	translateIn := make(chan transcript.TranslateInputOutput, 100)
	translateOut := make(chan transcript.TranslateInputOutput, 100)
	meta := translation.NewMetaTranslator(app.client, roomToken, langID, translateIn, translateOut)
	transSender := translation.NewTranslatedSender(client, translateOut)

	roomCtx, roomCancel := context.WithCancel(context.Background())

	rs := &roomState{
		client:      client,
		sender:      sender,
		audioWorker: audioWorker,
		meta:        meta,
		transSender: transSender,
		cancel:      roomCancel,
	}

	app.mu.Lock()
	app.rooms[roomToken] = rs
	app.mu.Unlock()

	go sender.Run(roomCtx)
	go audioWorker.Run(roomCtx)
	go transSender.Run(roomCtx)

	var lastErr error
	for i := 0; i < constants.MaxConnectTries; i++ {
		result, err := client.Connect(roomCtx, signaling.NoReconnect)
		switch result {
		case signaling.SigConnectSuccess:
			client.AddTarget(ncSessionID)
			slog.Info("connected to signaling server", "room_token", roomToken)
			return nil
		case signaling.SigConnectFailure:
			client.Close()
			roomCancel()
			app.mu.Lock()
			delete(app.rooms, roomToken)
			app.mu.Unlock()
			return fmt.Errorf("connection failed: %w", err)
		case signaling.SigConnectRetry:
			lastErr = err
			time.Sleep(2 * time.Second)
		}
	}

	return fmt.Errorf("failed to connect after %d attempts: %w", constants.MaxConnectTries, lastErr)
}

func (app *Application) LeaveCall(roomToken string) {
	app.mu.Lock()
	rs, ok := app.rooms[roomToken]
	app.mu.Unlock()

	if !ok {
		return
	}

	rs.client.Close()
}

func (app *Application) SetCallLanguage(roomToken, langID string) error {
	app.mu.Lock()
	rs, ok := app.rooms[roomToken]
	app.mu.Unlock()

	if !ok {
		slog.Info("set call language (no active room)", "room_token", roomToken, "lang_id", langID)
		return nil
	}

	rs.client.SetRoomLangID(langID)
	if err := rs.audioWorker.SetLanguage(langID); err != nil {
		slog.Error("failed to switch transcription language", "error", err, "room_token", roomToken, "lang_id", langID)
		return fmt.Errorf("failed to switch transcription language: %w", err)
	}

	if rs.meta != nil {
		rs.meta.SetRoomLangID(langID)
	}

	slog.Info("set call language", "room_token", roomToken, "lang_id", langID)
	return nil
}

func (app *Application) GetTranslationLanguages(roomToken string) (any, error) {
	app.mu.Lock()
	rs, ok := app.rooms[roomToken]
	app.mu.Unlock()

	if ok && rs.meta != nil {
		langs, err := rs.meta.GetTranslationLanguages()
		if err != nil {
			slog.Warn("failed to get translation languages from meta translator", "error", err)
		} else {
			return langs, nil
		}
	}

	tmp := translation.NewOCPTranslator(app.client, "en", "en", "languages-dummy")
	langs, err := tmp.GetTranslationLanguages()
	if err != nil {
		slog.Info("get translation languages", "room_token", roomToken)
		return map[string]any{
			"origin_languages": map[string]any{},
			"target_languages": map[string]any{},
		}, nil
	}
	return langs, nil
}

func (app *Application) GetTranslationLanguagesForCapabilities() *translation.SupportedTranslationLanguages {
	tmp := translation.NewOCPTranslator(app.client, "en", "en", "languages-dummy")
	langs, err := tmp.GetTranslationLanguages()
	if err != nil {
		return nil
	}
	return langs
}

func (app *Application) SetTargetLanguage(roomToken, ncSessionID string, langID *string) error {
	app.mu.Lock()
	rs, ok := app.rooms[roomToken]
	app.mu.Unlock()

	if !ok {
		slog.Info("set target language (no active room)", "room_token", roomToken)
		return nil
	}

	if langID == nil || *langID == "" {
		rs.meta.RemoveTranslator(ncSessionID)
		slog.Info("removed target language", "room_token", roomToken, "nc_session_id", ncSessionID)
		return nil
	}

	if err := rs.meta.AddTranslator(*langID, ncSessionID); err != nil {
		return fmt.Errorf("failed to set target language: %w", err)
	}

	slog.Info("set target language",
		"room_token", roomToken,
		"nc_session_id", ncSessionID,
		"lang_id", *langID,
	)
	return nil
}

func (app *Application) leaveCallCb(roomToken string) {
	app.mu.Lock()
	defer app.mu.Unlock()

	if rs, ok := app.rooms[roomToken]; ok {
		if rs.client.IsDefunct() {
			if rs.cancel != nil {
				rs.cancel()
			}
			if rs.meta != nil {
				rs.meta.Shutdown()
			}
			delete(app.rooms, roomToken)
			slog.Info("cleaned up defunct client", "room_token", roomToken)
		}
	}
}

func (app *Application) Shutdown() {
	app.mu.Lock()
	defer app.mu.Unlock()

	for token, rs := range app.rooms {
		rs.client.Close()
		if rs.cancel != nil {
			rs.cancel()
		}
		if rs.meta != nil {
			rs.meta.Shutdown()
		}
		delete(app.rooms, token)
	}
	slog.Info("application shutdown complete")
}
