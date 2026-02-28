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

type Sender struct {
	client *signaling.SpreedClient
	ch     chan signaling.Transcript
	logger *slog.Logger
}

func NewSender(client *signaling.SpreedClient, ch chan signaling.Transcript) *Sender {
	return &Sender{
		client: client,
		ch:     ch,
		logger: slog.With("component", "transcript_sender"),
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

			done := make(chan struct{})
			go func() {
				s.client.SendTranscript(t)
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
