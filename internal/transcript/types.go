// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package transcript

type TranslateInputOutput struct {
	OriginLanguage     string
	TargetLanguage     string
	Message            string
	SpeakerSessionID   string
	TargetNcSessionIDs map[string]struct{}
}
