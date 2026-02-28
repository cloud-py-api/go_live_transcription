// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package translation

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nextcloud/go_live_transcription/internal/appapi"
	"github.com/nextcloud/go_live_transcription/internal/constants"
	"github.com/nextcloud/go_live_transcription/internal/languages"
)

const translateTaskType = "core:text2text:translate"
const autoDetectOriginLangID = "detect_language"

var (
	ErrTranslateFatal    = errors.New("translation fatal error")
	ErrTranslateLangPair = errors.New("unsupported language pair")
	ErrTranslate         = errors.New("translation error")
)

type SupportedTranslationLanguages struct {
	OriginLanguages map[string]languages.LanguageModel `json:"origin_languages"`
	TargetLanguages map[string]languages.LanguageModel `json:"target_languages"`
}

type Task struct {
	ID     int               `json:"id"`
	Status string            `json:"status"`
	Output map[string]string `json:"output,omitempty"`
}

type TaskResponse struct {
	Task Task `json:"task"`
}

type InputShapeEnum struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type TaskType struct {
	InputShapeEnumValues map[string][]InputShapeEnum `json:"inputShapeEnumValues"`
}

type TaskTypesResponse struct {
	Types map[string]TaskType `json:"types"`
}

type OCPTranslator struct {
	mu              sync.Mutex
	client          *appapi.Client
	originLanguage  string
	targetLanguage  string
	roomToken       string
	ocpOriginLangID string
	ncSessionIDs    map[string]struct{} // NC session IDs receiving this translation
	taskTypesCache  *taskTypesCache
	logger          *slog.Logger
}

type taskTypesCache struct {
	time  time.Time
	types TaskTypesResponse
}

func NewOCPTranslator(client *appapi.Client, originLang, targetLang, roomToken string) *OCPTranslator {
	return &OCPTranslator{
		client:          client,
		originLanguage:  originLang,
		targetLanguage:  targetLang,
		roomToken:       roomToken,
		ocpOriginLangID: originLang,
		ncSessionIDs:    make(map[string]struct{}),
		logger: slog.With(
			"component", "ocp_translator",
			"origin_lang", originLang,
			"target_lang", targetLang,
		),
	}
}

func (t *OCPTranslator) AddSessionID(ncSessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ncSessionIDs[ncSessionID] = struct{}{}
}

func (t *OCPTranslator) RemoveSessionID(ncSessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.ncSessionIDs, ncSessionID)
}

func (t *OCPTranslator) SessionIDs() map[string]struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make(map[string]struct{}, len(t.ncSessionIDs))
	for k, v := range t.ncSessionIDs {
		result[k] = v
	}
	return result
}

func (t *OCPTranslator) HasSessions() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.ncSessionIDs) > 0
}

func (t *OCPTranslator) Translate(message string) (string, error) {
	schedBody := map[string]any{
		"type":     translateTaskType,
		"appId":    "go_live_transcription",
		"customId": fmt.Sprintf("lt-%s-%s-%s", t.roomToken, t.originLanguage, t.targetLanguage),
		"input": map[string]any{
			"input":           message,
			"origin_language": t.ocpOriginLangID,
			"target_language": t.targetLanguage,
		},
	}

	var lastErr error
	for tries := constants.OCPTaskProcSchedRetries; tries > 0; tries-- {
		data, err := t.client.OCSPost(
			"/ocs/v2.php/taskprocessing/tasks_consumer/schedule",
			"admin",
			schedBody,
		)
		if err != nil {
			lastErr = err
			t.logger.Warn("task scheduling failed, retrying", "error", err, "tries_left", tries-1)
			time.Sleep(2 * time.Second)
			continue
		}

		var resp TaskResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			return "", fmt.Errorf("%w: parse schedule response: %v", ErrTranslate, err)
		}

		result, err := t.pollTask(resp.Task.ID)
		if err != nil {
			return "", err
		}
		return result, nil
	}

	return "", fmt.Errorf("%w: failed after retries: %v", ErrTranslate, lastErr)
}

func (t *OCPTranslator) pollTask(taskID int) (string, error) {
	path := fmt.Sprintf("/ocs/v1.php/taskprocessing/tasks_consumer/task/%d", taskID)

	for i := 0; i < 360; i++ { // up to ~30 minutes
		if i < 180 {
			waitTime := min(1<<i, 5) // 1,2,4,5,5,5,...
			time.Sleep(time.Duration(waitTime) * time.Second)
		} else {
			time.Sleep(10 * time.Second)
		}

		data, err := t.client.OCSGet(path, "admin")
		if err != nil {
			t.logger.Warn("task poll error", "error", err, "poll_count", i)
			time.Sleep(5 * time.Second)
			continue
		}

		var resp TaskResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}

		switch resp.Task.Status {
		case "STATUS_SUCCESSFUL":
			if resp.Task.Output == nil {
				return "", fmt.Errorf("%w: no output in task result", ErrTranslate)
			}
			output, ok := resp.Task.Output["output"]
			if !ok {
				return "", fmt.Errorf("%w: 'output' key not found in task result", ErrTranslate)
			}
			return output, nil
		case "STATUS_FAILED":
			return "", fmt.Errorf("%w: task failed", ErrTranslate)
		}
	}

	return "", fmt.Errorf("%w: task timed out", ErrTranslate)
}

func (t *OCPTranslator) IsLanguagePairSupported() error {
	taskTypes, err := t.getTaskTypes()
	if err != nil {
		return err
	}

	tt, ok := taskTypes.Types[translateTaskType]
	if !ok {
		return fmt.Errorf("%w: no text2text translate task type available", ErrTranslateFatal)
	}

	originSupported := false
	autoDetectSupported := false
	for _, v := range tt.InputShapeEnumValues["origin_language"] {
		if v.Value == t.originLanguage {
			originSupported = true
		}
		if v.Value == autoDetectOriginLangID {
			autoDetectSupported = true
		}
	}
	if !originSupported {
		if !autoDetectSupported {
			return fmt.Errorf("%w: origin language '%s' not supported and no auto-detection",
				ErrTranslateLangPair, t.originLanguage)
		}
		t.ocpOriginLangID = autoDetectOriginLangID
	}

	targetSupported := false
	for _, v := range tt.InputShapeEnumValues["target_language"] {
		if v.Value == t.targetLanguage {
			targetSupported = true
			break
		}
	}
	if !targetSupported {
		return fmt.Errorf("%w: target language '%s' not supported",
			ErrTranslateLangPair, t.targetLanguage)
	}

	return nil
}

func (t *OCPTranslator) GetTranslationLanguages() (*SupportedTranslationLanguages, error) {
	taskTypes, err := t.getTaskTypes()
	if err != nil {
		return nil, err
	}

	tt, ok := taskTypes.Types[translateTaskType]
	if !ok {
		return nil, fmt.Errorf("%w: no text2text translate task type", ErrTranslateFatal)
	}

	olangs := make(map[string]languages.LanguageModel)
	for _, v := range tt.InputShapeEnumValues["origin_language"] {
		if lm, ok := languages.VoskSupportedLanguageMap[v.Value]; ok {
			olangs[v.Value] = lm
		} else {
			olangs[v.Value] = languages.LanguageModel{
				Name:     v.Value,
				Metadata: languages.LanguageMetadata{Separator: " "},
			}
		}
	}

	tlangs := make(map[string]languages.LanguageModel)
	for _, v := range tt.InputShapeEnumValues["target_language"] {
		if lm, ok := languages.LanguageMap[v.Value]; ok {
			tlangs[v.Value] = lm
		} else {
			tlangs[v.Value] = languages.LanguageModel{
				Name:     v.Value,
				Metadata: languages.LanguageMetadata{Separator: " "},
			}
		}
	}

	return &SupportedTranslationLanguages{
		OriginLanguages: olangs,
		TargetLanguages: tlangs,
	}, nil
}

func (t *OCPTranslator) getTaskTypes() (*TaskTypesResponse, error) {
	if t.taskTypesCache != nil && time.Since(t.taskTypesCache.time) < constants.CacheTranslationTaskTypes {
		return &t.taskTypesCache.types, nil
	}

	data, err := t.client.OCSGet("/ocs/v2.php/taskprocessing/tasks_consumer/tasktypes", "admin")
	if err != nil {
		return nil, fmt.Errorf("%w: fetch task types: %v", ErrTranslateFatal, err)
	}

	var resp TaskTypesResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("%w: parse task types: %v", ErrTranslate, err)
	}

	if _, ok := resp.Types[translateTaskType]; !ok {
		return nil, fmt.Errorf("%w: no text2text translate provider installed", ErrTranslateFatal)
	}

	t.taskTypesCache = &taskTypesCache{time: time.Now(), types: resp}
	return &resp, nil
}
