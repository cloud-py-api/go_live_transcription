// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package transcript

import (
	"context"
	"log/slog"
	"time"

	"github.com/nextcloud/go_live_transcription/internal/constants"
	"github.com/nextcloud/go_live_transcription/internal/signaling"
)

type TranslationForwarder interface {
	ShouldTranslate() bool
	IsTranslationTarget(ncSessionID string) bool
}

type Sender struct {
	client      *signaling.SpreedClient
	ch          chan signaling.Transcript
	translateIn chan TranslateInputOutput
	translator  TranslationForwarder
	logger      *slog.Logger
}

func NewSender(
	client *signaling.SpreedClient,
	ch chan signaling.Transcript,
	translateIn chan TranslateInputOutput,
	translator TranslationForwarder,
) *Sender {
	return &Sender{
		client:      client,
		ch:          ch,
		translateIn: translateIn,
		translator:  translator,
		logger:      slog.With("component", "transcript_sender"),
	}
}

func (s *Sender) Run(ctx context.Context) {
	s.logger.Debug("transcript sender started")
	defer s.logger.Debug("transcript sender stopped")

	timeout := constants.SendTimeout
	timeoutCount := 0

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-s.ch:
			if s.client.IsDefunct() {
				time.Sleep(2 * time.Second)
				continue
			}

			// Forward final transcripts to the translation pipeline
			if t.Final && s.translator.ShouldTranslate() {
				select {
				case s.translateIn <- TranslateInputOutput{
					OriginLanguage:   t.LangID,
					Message:          t.Message,
					SpeakerSessionID: t.SpeakerSessionID,
				}:
				default:
					s.logger.Warn("translate input channel full, dropping")
				}
			}

			// For final transcripts, skip translation targets — they
			// will receive the translated version instead.
			var exclude func(string) bool
			if t.Final && s.translator.ShouldTranslate() {
				exclude = s.translator.IsTranslationTarget
			}

			done := make(chan struct{})
			go func() {
				s.client.SendTranscript(t, exclude)
				close(done)
			}()

			select {
			case <-done:
				if timeoutCount > 0 {
					timeoutCount--
				}
				if timeoutCount == 0 && timeout > constants.SendTimeout {
					timeout = max(constants.SendTimeout, time.Duration(float64(timeout)/constants.TimeoutIncreaseFactor))
				}
			case <-time.After(timeout):
				s.logger.Error("timeout sending transcript",
					"speaker_session_id", t.SpeakerSessionID,
					"timeout", timeout,
				)
				if timeout <= constants.MaxTranscriptSendTimeout {
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
