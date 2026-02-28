// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package translation

import (
	"context"
	"log/slog"
	"time"

	"github.com/nextcloud/go_live_transcription/internal/constants"
	"github.com/nextcloud/go_live_transcription/internal/signaling"
	"github.com/nextcloud/go_live_transcription/internal/transcript"
)

type TranslatedSender struct {
	client *signaling.SpreedClient
	ch     chan transcript.TranslateInputOutput
	logger *slog.Logger
}

func NewTranslatedSender(client *signaling.SpreedClient, ch chan transcript.TranslateInputOutput) *TranslatedSender {
	return &TranslatedSender{
		client: client,
		ch:     ch,
		logger: slog.With("component", "translated_sender"),
	}
}

func (s *TranslatedSender) Run(ctx context.Context) {
	s.logger.Debug("translated text sender started")
	defer s.logger.Debug("translated text sender stopped")

	timeout := constants.SendTimeout
	timeoutCount := 0

	for {
		select {
		case <-ctx.Done():
			return
		case seg := <-s.ch:
			done := make(chan struct{})
			go func() {
				s.sendTranslatedText(seg)
				close(done)
			}()

			select {
			case <-done:
				if timeoutCount > 0 {
					timeoutCount--
				}
				if timeoutCount == 0 && timeout > constants.SendTimeout {
					newTimeout := time.Duration(float64(timeout) / constants.TimeoutIncreaseFactor)
					if newTimeout > constants.SendTimeout {
						timeout = newTimeout
					} else {
						timeout = constants.SendTimeout
					}
				}
			case <-time.After(timeout):
				s.logger.Warn("timeout sending translated text",
					"target_lang", seg.TargetLanguage,
					"timeout", timeout,
				)
				if timeout <= constants.MaxTranslationSendTimeout {
					timeoutCount++
					if timeoutCount >= 5 {
						timeout = time.Duration(float64(timeout) * constants.TimeoutIncreaseFactor)
						timeoutCount = 0
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}
}

func (s *TranslatedSender) sendTranslatedText(seg transcript.TranslateInputOutput) {
	for ncSid := range seg.TargetNcSessionIDs {
		finalVal := true
		s.client.SendMessage(signaling.SignalingMessage{
			Type: "message",
			Message: &signaling.DataMessage{
				Recipient: &signaling.Recipient{Type: "session", SessionID: ncSid},
				Data: &signaling.MessagePayload{
					LangID:           seg.TargetLanguage,
					Message:          seg.Message,
					SpeakerSessionID: seg.SpeakerSessionID,
					Final:            &finalVal,
					Type:             "transcript",
				},
			},
		})
	}
}
