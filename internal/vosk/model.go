// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package vosk

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	vosk "github.com/alphacep/vosk-api/go"

	"github.com/nextcloud/go_live_transcription/internal/appapi"
	"github.com/nextcloud/go_live_transcription/internal/languages"
)

type ModelManager struct {
	mu     sync.Mutex
	models map[string]*modelEntry
	logger *slog.Logger
}

type modelEntry struct {
	model    *vosk.VoskModel
	refCount int
}

var globalModelManager *ModelManager
var modelManagerOnce sync.Once

func GetModelManager() *ModelManager {
	modelManagerOnce.Do(func() {
		vosk.SetLogLevel(-1) // suppress vosk's own logs
		globalModelManager = &ModelManager{
			models: make(map[string]*modelEntry),
			logger: slog.With("component", "model_manager"),
		}
	})
	return globalModelManager
}

func (mm *ModelManager) GetModel(lang string) (*vosk.VoskModel, error) {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	if entry, ok := mm.models[lang]; ok {
		entry.refCount++
		mm.logger.Info("reusing cached model", "lang", lang, "ref_count", entry.refCount)
		return entry.model, nil
	}

	modelDir, ok := languages.ModelsList[lang]
	if !ok {
		return nil, fmt.Errorf("no model available for language: %s", lang)
	}

	modelPath := filepath.Join(appapi.PersistentStorage(), modelDir)
	if _, err := os.Stat(modelPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("model directory not found: %s", modelPath)
	}

	mm.logger.Info("loading vosk model", "lang", lang, "path", modelPath)
	model, err := vosk.NewModel(modelPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load vosk model for %s: %w", lang, err)
	}

	mm.models[lang] = &modelEntry{model: model, refCount: 1}
	mm.logger.Info("vosk model loaded", "lang", lang)
	return model, nil
}

func (mm *ModelManager) ReleaseModel(lang string) {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	entry, ok := mm.models[lang]
	if !ok {
		return
	}

	entry.refCount--
	mm.logger.Info("released model", "lang", lang, "ref_count", entry.refCount)

	if entry.refCount <= 0 {
		entry.model.Free()
		delete(mm.models, lang)
		mm.logger.Info("freed vosk model", "lang", lang)
	}
}

func (mm *ModelManager) IsModelAvailable(lang string) bool {
	modelDir, ok := languages.ModelsList[lang]
	if !ok {
		return false
	}
	modelPath := filepath.Join(appapi.PersistentStorage(), modelDir)
	info, err := os.Stat(modelPath)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func (mm *ModelManager) ListAvailableModels() []string {
	var available []string
	for lang := range languages.ModelsList {
		if mm.IsModelAvailable(lang) {
			available = append(available, lang)
		}
	}
	return available
}
