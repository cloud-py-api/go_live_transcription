// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package translation

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nextcloud/go_live_transcription/internal/appapi"
	"github.com/nextcloud/go_live_transcription/internal/constants"
	"github.com/nextcloud/go_live_transcription/internal/transcript"
)

type MetaTranslator struct {
	mu              sync.Mutex
	translators     map[string]*OCPTranslator // key: target language
	sidLangMap      map[string]string         // NC session ID → target language
	client          *appapi.Client
	roomToken       string
	roomLangID      string
	shouldTranslate atomic.Bool
	translateIn     chan transcript.TranslateInputOutput
	translateOut    chan transcript.TranslateInputOutput
	langsCache      *langsCache
	cancel          context.CancelFunc
	logger          *slog.Logger
}

type langsCache struct {
	time  time.Time
	langs *SupportedTranslationLanguages
}

func NewMetaTranslator(
	client *appapi.Client,
	roomToken, roomLangID string,
	translateIn chan transcript.TranslateInputOutput,
	translateOut chan transcript.TranslateInputOutput,
) *MetaTranslator {
	return &MetaTranslator{
		translators:  make(map[string]*OCPTranslator),
		sidLangMap:   make(map[string]string),
		client:       client,
		roomToken:    roomToken,
		roomLangID:   roomLangID,
		translateIn:  translateIn,
		translateOut: translateOut,
		logger:       slog.With("component", "meta_translator", "room_token", roomToken),
	}
}

func (mt *MetaTranslator) ShouldTranslate() bool {
	return mt.shouldTranslate.Load()
}

func (mt *MetaTranslator) AddTranslator(targetLangID, ncSessionID string) error {
	mt.mu.Lock()
	defer mt.mu.Unlock()

	if existingLang, ok := mt.sidLangMap[ncSessionID]; ok {
		if existingLang == targetLangID {
			return nil
		}
		mt.removeTranslatorLocked(existingLang, ncSessionID)
	}
	mt.sidLangMap[ncSessionID] = targetLangID

	if _, ok := mt.translators[targetLangID]; !ok {
		translator := NewOCPTranslator(mt.client, mt.roomLangID, targetLangID, mt.roomToken)
		if err := translator.IsLanguagePairSupported(); err != nil {
			delete(mt.sidLangMap, ncSessionID)
			return err
		}
		mt.translators[targetLangID] = translator
	}

	mt.translators[targetLangID].AddSessionID(ncSessionID)
	mt.shouldTranslate.Store(true)

	mt.ensureRunning()

	mt.logger.Info("added translator",
		"target_lang", targetLangID,
		"nc_session_id", ncSessionID,
	)
	return nil
}

func (mt *MetaTranslator) IsTranslationTarget(ncSessionID string) bool {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	_, ok := mt.sidLangMap[ncSessionID]
	return ok
}

func (mt *MetaTranslator) IsTranslating() bool {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	return len(mt.sidLangMap) > 0
}

func (mt *MetaTranslator) IsTargetLangSupported(targetLangID string) (bool, error) {
	tmp := NewOCPTranslator(mt.client, mt.roomLangID, targetLangID, mt.roomToken)
	err := tmp.IsLanguagePairSupported()
	if err != nil {
		return false, err
	}
	return true, nil
}

func (mt *MetaTranslator) RemoveTranslator(ncSessionID string) {
	mt.mu.Lock()
	defer mt.mu.Unlock()

	langID, ok := mt.sidLangMap[ncSessionID]
	if !ok {
		return
	}
	mt.removeTranslatorLocked(langID, ncSessionID)
	delete(mt.sidLangMap, ncSessionID)

	if len(mt.sidLangMap) == 0 {
		mt.shouldTranslate.Store(false)
		mt.stopRunning()
	}
}

func (mt *MetaTranslator) removeTranslatorLocked(targetLangID, ncSessionID string) {
	translator, ok := mt.translators[targetLangID]
	if !ok {
		return
	}
	translator.RemoveSessionID(ncSessionID)
	if !translator.HasSessions() {
		delete(mt.translators, targetLangID)
	}
}

func (mt *MetaTranslator) GetTranslationLanguages() (*SupportedTranslationLanguages, error) {
	mt.mu.Lock()
	defer mt.mu.Unlock()

	if mt.langsCache != nil && time.Since(mt.langsCache.time) < constants.CacheTranslationLangsFor {
		return mt.langsCache.langs, nil
	}

	tmp := NewOCPTranslator(mt.client, mt.roomLangID, "en", mt.roomToken)
	langs, err := tmp.GetTranslationLanguages()
	if err != nil {
		return nil, err
	}

	mt.langsCache = &langsCache{time: time.Now(), langs: langs}
	return langs, nil
}

func (mt *MetaTranslator) SetRoomLangID(langID string) {
	mt.mu.Lock()
	defer mt.mu.Unlock()

	if mt.roomLangID == langID {
		return
	}

	mt.roomLangID = langID
	mt.langsCache = nil // invalidate cache

	for targetLang, oldTranslator := range mt.translators {
		newTranslator := NewOCPTranslator(mt.client, langID, targetLang, mt.roomToken)
		for sid := range oldTranslator.SessionIDs() {
			newTranslator.AddSessionID(sid)
		}
		mt.translators[targetLang] = newTranslator
	}

	mt.logger.Info("room language updated", "lang_id", langID)
}

func (mt *MetaTranslator) Shutdown() {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	mt.shouldTranslate.Store(false)
	mt.stopRunning()
}

func (mt *MetaTranslator) ensureRunning() {
	if mt.cancel != nil {
		return // already running
	}
	ctx, cancel := context.WithCancel(context.Background())
	mt.cancel = cancel
	go mt.runTranslation(ctx)
}

func (mt *MetaTranslator) stopRunning() {
	if mt.cancel != nil {
		mt.cancel()
		mt.cancel = nil
	}
}

func (mt *MetaTranslator) runTranslation(ctx context.Context) {
	mt.logger.Debug("translation goroutine started")
	defer mt.logger.Debug("translation goroutine stopped")

	for {
		select {
		case <-ctx.Done():
			return
		case segment := <-mt.translateIn:
			mt.mu.Lock()
			for _, translator := range mt.translators {
				seg := segment
				seg.TargetLanguage = translator.targetLanguage
				seg.TargetNcSessionIDs = translator.SessionIDs()

				go mt.handleTranslation(translator, seg)
			}
			mt.mu.Unlock()
		}
	}
}

func (mt *MetaTranslator) handleTranslation(translator *OCPTranslator, seg transcript.TranslateInputOutput) {
	translated, err := translator.Translate(seg.Message)
	if err != nil {
		mt.logger.Error("translation failed",
			"error", err,
			"origin_lang", seg.OriginLanguage,
			"target_lang", seg.TargetLanguage,
		)
		return
	}

	seg.Message = translated
	select {
	case mt.translateOut <- seg:
	default:
		mt.logger.Warn("translate output channel full")
	}
}
