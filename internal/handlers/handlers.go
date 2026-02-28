// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/nextcloud/go_live_transcription/internal/appapi"
	"github.com/nextcloud/go_live_transcription/internal/languages"
	"github.com/nextcloud/go_live_transcription/internal/service"
	"github.com/nextcloud/go_live_transcription/internal/vosk"
)

type Handler struct {
	Config  *appapi.Config
	Client  *appapi.Client
	Service *service.Application
	Enabled atomic.Bool
}

func NewHandler(cfg *appapi.Config, client *appapi.Client, svc *service.Application) *Handler {
	return &Handler{
		Config:  cfg,
		Client:  client,
		Service: svc,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("failed to write JSON response", "error", err)
	}
}

func (h *Handler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, StatusResponse{Status: "ok"})
}

func (h *Handler) SetEnabled(w http.ResponseWriter, r *http.Request) {
	enabledParam := r.URL.Query().Get("enabled")
	enabled := enabledParam == "1" || enabledParam == "true"

	h.Enabled.Store(enabled)
	slog.Info("app enabled state changed", "enabled", enabled)
	writeJSON(w, http.StatusOK, ErrorResponse{Error: ""})
}

func (h *Handler) GetEnabled(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, EnabledResponse{Enabled: h.Enabled.Load()})
}

func (h *Handler) Init(w http.ResponseWriter, r *http.Request) {
	slog.Info("init called")
	writeJSON(w, http.StatusOK, struct{}{})

	// Download models and report init completion in background
	go func() {
		storageDir := appapi.PersistentStorage()
		if err := vosk.DownloadModels(h.Client, storageDir); err != nil {
			slog.Error("model download failed", "error", err)
			if statusErr := h.Client.SetInitStatus(-1); statusErr != nil {
				slog.Error("failed to report init failure", "error", statusErr)
			}
			return
		}

		if err := h.Client.SetInitStatus(100); err != nil {
			slog.Error("failed to report init status", "error", err)
		}
	}()
}

func (h *Handler) GetLanguages(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, languages.VoskSupportedLanguageMap)
}

func (h *Handler) GetCapabilities(w http.ResponseWriter, r *http.Request) {
	features := []string{"live_transcription"}
	appCaps := map[string]any{
		"version": h.Config.AppVersion,
		"live_transcription": map[string]any{
			"supported_languages": languages.VoskSupportedLanguageMap,
		},
	}

	translationLangs := h.Service.GetTranslationLanguagesForCapabilities()
	if translationLangs != nil {
		features = append(features, "live_translation")
		appCaps["live_translation"] = map[string]any{
			"supported_translation_languages": translationLangs,
		}
	}

	appCaps["features"] = features

	writeJSON(w, http.StatusOK, map[string]any{
		h.Config.AppID: appCaps,
	})
}

func (h *Handler) TranscribeCall(w http.ResponseWriter, r *http.Request) {
	var req TranscribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	enable := true
	if req.Enable != nil {
		enable = *req.Enable
	}
	langID := req.LangID
	if langID == "" {
		langID = "en"
	}

	if err := h.Service.TranscriptReq(r.Context(), req.RoomToken, req.NcSessionID, langID, enable); err != nil {
		slog.Error("transcribe request failed", "error", err, "room_token", req.RoomToken)
		writeJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, MessageResponse{Message: "Transcription request processed successfully."})
}

func (h *Handler) LeaveCall(w http.ResponseWriter, r *http.Request) {
	var req LeaveCallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	h.Service.LeaveCall(req.RoomToken)
	writeJSON(w, http.StatusOK, MessageResponse{Message: "Leave call request processed."})
}

func (h *Handler) SetCallLanguage(w http.ResponseWriter, r *http.Request) {
	var req RoomLanguageSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	if req.LangID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Invalid or unsupported language ID provided."})
		return
	}
	if _, ok := languages.VoskSupportedLanguageMap[req.LangID]; !ok {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "Invalid or unsupported language ID provided."})
		return
	}

	if err := h.Service.SetCallLanguage(req.RoomToken, req.LangID); err != nil {
		slog.Error("set call language failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Failed to set language for the call"})
		return
	}

	writeJSON(w, http.StatusOK, MessageResponse{Message: "Language set successfully for the call"})
}

func (h *Handler) GetTranslationLanguages(w http.ResponseWriter, r *http.Request) {
	roomToken := r.URL.Query().Get("roomToken")
	langs, err := h.Service.GetTranslationLanguages(roomToken)
	if err != nil {
		slog.Error("get translation languages failed", "error", err)
		writeJSON(w, http.StatusInternalServerError,
			ErrorResponse{Error: "An error occurred while fetching translation languages."})
		return
	}
	writeJSON(w, http.StatusOK, langs)
}

func (h *Handler) SetTargetLanguage(w http.ResponseWriter, r *http.Request) {
	var req TargetLanguageSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}

	if err := h.Service.SetTargetLanguage(req.RoomToken, req.NcSessionID, req.LangID); err != nil {
		slog.Error("set target language failed", "error", err)
		writeJSON(w, http.StatusInternalServerError,
			ErrorResponse{Error: "Failed to set the target translation language for the participant."})
		return
	}

	writeJSON(w, http.StatusOK,
		MessageResponse{Message: "Target translation language set successfully for the participant."})
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /heartbeat", h.Heartbeat)
	mux.HandleFunc("PUT /enabled", h.SetEnabled)
	mux.HandleFunc("GET /enabled", h.GetEnabled)
	mux.HandleFunc("POST /init", h.Init)
	mux.HandleFunc("GET /capabilities", h.GetCapabilities)

	mux.HandleFunc("GET /api/v1/languages", h.GetLanguages)
	mux.HandleFunc("POST /api/v1/call/transcribe", h.TranscribeCall)
	mux.HandleFunc("POST /api/v1/call/leave", h.LeaveCall)
	mux.HandleFunc("POST /api/v1/call/set-language", h.SetCallLanguage)
	mux.HandleFunc("GET /api/v1/translation/languages", h.GetTranslationLanguages)
	mux.HandleFunc("POST /api/v1/translation/set-target-language", h.SetTargetLanguage)
}
