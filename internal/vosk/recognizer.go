// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package vosk

/*
#include <malloc.h>
*/
import "C"

import (
	"encoding/json"
	"log/slog"
	"sync"

	vosk "github.com/alphacep/vosk-api/go"
	"github.com/nextcloud/go_live_transcription/internal/signaling"
)

type voskResult struct {
	Partial string `json:"partial,omitempty"`
	Text    string `json:"text,omitempty"`
}

// maxChunksBeforeForceFinalize forces a FinalResult() call after this many
// chunks without a natural final result, preventing unbounded memory growth.
// At 16kHz with 320-sample chunks (20ms each), 500 chunks = 10 seconds.
const maxChunksBeforeForceFinalize = 500

type Recognizer struct {
	mu               sync.Mutex
	rec              *vosk.VoskRecognizer
	model            *vosk.VoskModel
	sampleRate       float64
	sessionID        string
	language         string
	feedCount        int64
	chunksSinceFinal int
	transcriptCh     chan signaling.Transcript
	logger           *slog.Logger
}

func NewRecognizer(model *vosk.VoskModel, sessionID, language string, sampleRate float64, transcriptCh chan signaling.Transcript) (*Recognizer, error) {
	rec, err := vosk.NewRecognizer(model, sampleRate)
	if err != nil {
		return nil, err
	}
	rec.SetWords(0) // no word-level timing

	return &Recognizer{
		rec:          rec,
		model:        model,
		sampleRate:   sampleRate,
		sessionID:    sessionID,
		language:     language,
		transcriptCh: transcriptCh,
		logger:       slog.With("session_id", sessionID, "component", "vosk_recognizer"),
	}, nil
}

func (r *Recognizer) FeedAudio(pcmData []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.rec == nil {
		return
	}

	r.feedCount++
	r.chunksSinceFinal++

	if r.rec.AcceptWaveform(pcmData) != 0 {
		// Natural final result
		resultJSON := r.rec.Result()
		r.logger.Debug("vosk final result", "json", resultJSON)
		r.emitTranscript(resultJSON, true)
		r.chunksSinceFinal = 0
	} else if r.chunksSinceFinal >= maxChunksBeforeForceFinalize {
		// Force finalization to prevent unbounded C-side memory growth
		resultJSON := r.rec.FinalResult()
		r.logger.Debug("vosk forced final", "json", resultJSON, "chunks", r.chunksSinceFinal)
		r.emitTranscript(resultJSON, true)
		r.chunksSinceFinal = 0
		// Recreate the recognizer to fully release C memory
		r.resetRecognizer()
	} else {
		// Partial result
		partialJSON := r.rec.PartialResult()
		r.emitTranscript(partialJSON, false)
	}
}

func (r *Recognizer) emitTranscript(resultJSON string, isFinal bool) {
	var result voskResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return
	}

	var message string
	if isFinal {
		message = result.Text
	} else {
		message = result.Partial
	}

	if message == "" || message == "the" {
		return
	}

	select {
	case r.transcriptCh <- signaling.Transcript{
		Final:            isFinal,
		LangID:           r.language,
		Message:          message,
		SpeakerSessionID: r.sessionID,
	}:
	default:
		r.logger.Warn("transcript channel full, dropping message")
	}
}

// Must be called with r.mu held.
func (r *Recognizer) resetRecognizer() {
	if r.rec != nil {
		r.rec.Free()
	}
	// Force glibc to return freed pages to OS
	C.malloc_trim(0)

	newRec, err := vosk.NewRecognizer(r.model, r.sampleRate)
	if err != nil {
		r.logger.Error("failed to recreate recognizer", "error", err)
		r.rec = nil
		return
	}
	newRec.SetWords(0)
	r.rec = newRec
	r.logger.Debug("recognizer reset")
}

func (r *Recognizer) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.rec != nil {
		r.rec.Free()
		r.rec = nil
	}
	r.logger.Debug("recognizer closed")
}

type TranscriberManager struct {
	mu           sync.Mutex
	recognizers  map[string]*Recognizer
	language     string
	sampleRate   float64
	transcriptCh chan signaling.Transcript
	logger       *slog.Logger
}

func NewTranscriberManager(language string, sampleRate float64, transcriptCh chan signaling.Transcript) *TranscriberManager {
	return &TranscriberManager{
		recognizers:  make(map[string]*Recognizer),
		language:     language,
		sampleRate:   sampleRate,
		transcriptCh: transcriptCh,
		logger:       slog.With("component", "transcriber_manager"),
	}
}

func (tm *TranscriberManager) GetOrCreate(sessionID string) (*Recognizer, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if r, ok := tm.recognizers[sessionID]; ok {
		return r, nil
	}

	model, err := GetModelManager().GetModel(tm.language)
	if err != nil {
		return nil, err
	}

	r, err := NewRecognizer(model, sessionID, tm.language, tm.sampleRate, tm.transcriptCh)
	if err != nil {
		GetModelManager().ReleaseModel(tm.language)
		return nil, err
	}

	tm.recognizers[sessionID] = r
	tm.logger.Info("created recognizer", "session_id", sessionID, "language", tm.language)
	return r, nil
}

func (tm *TranscriberManager) Remove(sessionID string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if r, ok := tm.recognizers[sessionID]; ok {
		r.Close()
		GetModelManager().ReleaseModel(tm.language)
		delete(tm.recognizers, sessionID)
	}
}

func (tm *TranscriberManager) SetLanguage(language string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if language == tm.language {
		return nil
	}

	newModel, err := GetModelManager().GetModel(language)
	if err != nil {
		return err
	}

	for sid, r := range tm.recognizers {
		r.Close()
		GetModelManager().ReleaseModel(tm.language)
		delete(tm.recognizers, sid)
	}

	// Release model ref; recognizers will re-acquire on demand
	GetModelManager().ReleaseModel(language)
	_ = newModel

	tm.language = language
	tm.logger.Info("language switched", "language", language)
	return nil
}

func (tm *TranscriberManager) CloseAll() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	for sid, r := range tm.recognizers {
		r.Close()
		GetModelManager().ReleaseModel(tm.language)
		delete(tm.recognizers, sid)
	}
}
