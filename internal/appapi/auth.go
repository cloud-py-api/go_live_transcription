// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package appapi

import (
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"
)

func AuthMiddleware(cfg *Config, skipPaths map[string]bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if skipPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		exAppID := r.Header.Get("EX-APP-ID")
		authHeader := r.Header.Get("AUTHORIZATION-APP-API")

		if exAppID == "" || authHeader == "" {
			slog.Warn("missing auth headers", "path", r.URL.Path, "ex_app_id", exAppID)
			http.Error(w, `{"error": "missing authentication headers"}`, http.StatusUnauthorized)
			return
		}

		if exAppID != cfg.AppID {
			slog.Warn("invalid EX-APP-ID", "got", exAppID, "expected", cfg.AppID)
			http.Error(w, `{"error": "invalid EX-APP-ID"}`, http.StatusUnauthorized)
			return
		}

		username, secret := decodeAuthHeader(authHeader)
		if secret != cfg.AppSecret {
			slog.Warn("invalid app secret", "username", username)
			http.Error(w, `{"error": "invalid app secret"}`, http.StatusUnauthorized)
			return
		}

		r.Header.Set("X-Auth-Username", username)
		next.ServeHTTP(w, r)
	})
}

func decodeAuthHeader(header string) (username, secret string) {
	decoded, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		return "", ""
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}
