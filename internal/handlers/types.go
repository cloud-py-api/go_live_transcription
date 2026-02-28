// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

type TranscribeRequest struct {
	RoomToken               string  `json:"roomToken"`
	NcSessionID             string  `json:"ncSessionId"`
	Enable                  *bool   `json:"enable,omitempty"`
	LangID                  string  `json:"langId,omitempty"`
	TranslationTargetLangID *string `json:"translationTargetLangId,omitempty"`
}

type RoomLanguageSetRequest struct {
	RoomToken string `json:"roomToken"`
	LangID    string `json:"langId"`
}

type TargetLanguageSetRequest struct {
	RoomToken   string  `json:"roomToken"`
	NcSessionID string  `json:"ncSessionId"`
	LangID      *string `json:"langId,omitempty"`
}

type LeaveCallRequest struct {
	RoomToken string `json:"roomToken"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type MessageResponse struct {
	Message string `json:"message"`
}

type StatusResponse struct {
	Status string `json:"status"`
}

type EnabledResponse struct {
	Enabled bool `json:"enabled"`
}
