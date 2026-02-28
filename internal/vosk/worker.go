// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package vosk

import (
	"context"
	"encoding/binary"
	"log/slog"

	"github.com/nextcloud/go_live_transcription/internal/signaling"
)

type AudioWorker struct {
	client  *signaling.SpreedClient
	manager *TranscriberManager
	logger  *slog.Logger
}

func NewAudioWorker(client *signaling.SpreedClient, manager *TranscriberManager) *AudioWorker {
	return &AudioWorker{
		client:  client,
		manager: manager,
		logger:  slog.With("component", "audio_worker"),
	}
}

func (w *AudioWorker) Run(ctx context.Context) {
	w.logger.Debug("audio worker started")
	defer func() {
		w.manager.CloseAll()
		w.logger.Debug("audio worker stopped")
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case audio := <-w.client.PCMAudioCh:
			if len(audio.Samples) == 0 {
				continue
			}

			rec, err := w.manager.GetOrCreate(audio.SessionID)
			if err != nil {
				w.logger.Error("failed to get/create recognizer",
					"error", err,
					"session_id", audio.SessionID,
				)
				continue
			}

			downsampled := downsample48to16(audio.Samples)
			pcmBytes := int16ToBytes(downsampled)
			rec.FeedAudio(pcmBytes)
		}
	}
}

func int16ToBytes(samples []int16) []byte {
	buf := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(s))
	}
	return buf
}

func (w *AudioWorker) SetLanguage(language string) error {
	return w.manager.SetLanguage(language)
}

func downsample48to16(samples []int16) []int16 {
	const ratio = 3 // 48000 / 16000
	outLen := len(samples) / ratio
	out := make([]int16, outLen)
	for i := 0; i < outLen; i++ {
		sum := int32(samples[i*ratio]) + int32(samples[i*ratio+1]) + int32(samples[i*ratio+2])
		out[i] = int16(sum / ratio)
	}
	return out
}
