// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package constants

import "time"

const (
	MsgReceiveTimeout         = 10 * time.Second
	MaxConnectTries           = 5
	MaxAudioFrames            = 20
	MinTranscriptSendInterval = 300 * time.Millisecond
	HPBShutdownTimeout        = 30 * time.Second
	CallLeaveTimeout          = 60 * time.Second
	VoskConnectTimeout        = 60 * time.Second
	HPBPingTimeout            = 120 * time.Second
	OCPTaskProcSchedRetries   = 3
	OCPTaskTimeout            = 30 * time.Second
	SendTimeout               = 10 * time.Second
	TimeoutIncreaseFactor     = 1.5
	CacheTranslationLangsFor  = 15 * time.Minute
	CacheTranslationTaskTypes = 15 * time.Minute
	MaxTranscriptSendTimeout  = 30 * time.Second
	MaxTranslationSendTimeout = 60 * time.Second
)
