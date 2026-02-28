// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package appapi

import (
	"fmt"
	"os"
)

type Config struct {
	AppID          string
	AppSecret      string
	AppVersion     string
	AppPort        string
	NextcloudURL   string
	HPBUrl         string
	InternalSecret string
}

func LoadConfig() (*Config, error) {
	cfg := &Config{
		AppID:          os.Getenv("APP_ID"),
		AppSecret:      os.Getenv("APP_SECRET"),
		AppVersion:     os.Getenv("APP_VERSION"),
		AppPort:        os.Getenv("APP_PORT"),
		NextcloudURL:   os.Getenv("NEXTCLOUD_URL"),
		HPBUrl:         os.Getenv("LT_HPB_URL"),
		InternalSecret: os.Getenv("LT_INTERNAL_SECRET"),
	}

	if cfg.AppID == "" {
		return nil, fmt.Errorf("APP_ID environment variable is required")
	}
	if cfg.AppSecret == "" {
		return nil, fmt.Errorf("APP_SECRET environment variable is required")
	}
	if cfg.AppPort == "" {
		cfg.AppPort = "23000"
	}
	if cfg.AppVersion == "" {
		cfg.AppVersion = "0.0.1"
	}

	return cfg, nil
}

func PersistentStorage() string {
	path := os.Getenv("APP_PERSISTENT_STORAGE")
	if path == "" {
		return "/nc_app_live_transcription_data"
	}
	return path
}
