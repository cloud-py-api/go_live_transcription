// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package vosk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/nextcloud/go_live_transcription/internal/appapi"
)

const (
	hfRepo     = "Nextcloud-AI/vosk-models"
	hfRevision = "06f2f156dcd79092400891afb6cf8101e54f6ba2"
	hfAPIBase  = "https://huggingface.co/api/models"
	hfResolve  = "https://huggingface.co"
)

type hfEntry struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func DownloadModels(client *appapi.Client, storageDir string) error {
	slog.Info("starting model download", "repo", hfRepo, "dest", storageDir)

	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return fmt.Errorf("create storage dir: %w", err)
	}

	files, err := listAllFiles("")
	if err != nil {
		return fmt.Errorf("list repo files: %w", err)
	}

	slog.Info("found files to download", "total", len(files))

	var toDownload []hfEntry
	for _, f := range files {
		localPath := filepath.Join(storageDir, f.Path)
		if info, err := os.Stat(localPath); err == nil && info.Size() == f.Size {
			continue // already downloaded
		}
		toDownload = append(toDownload, f)
	}

	if len(toDownload) == 0 {
		slog.Info("all models already downloaded")
		return nil
	}

	slog.Info("downloading models", "files", len(toDownload), "skipped", len(files)-len(toDownload))

	for i, f := range toDownload {
		progress := int(float64(i) / float64(len(toDownload)) * 99)
		if err := client.SetInitStatus(progress); err != nil {
			slog.Warn("failed to report init progress", "error", err, "progress", progress)
		}

		if err := downloadFile(storageDir, f.Path); err != nil {
			return fmt.Errorf("download %s: %w", f.Path, err)
		}

		if (i+1)%50 == 0 {
			slog.Info("download progress", "completed", i+1, "total", len(toDownload))
		}
	}

	slog.Info("model download complete", "files", len(toDownload))
	return nil
}

func listAllFiles(prefix string) ([]hfEntry, error) {
	url := fmt.Sprintf("%s/%s/tree/%s", hfAPIBase, hfRepo, hfRevision)
	if prefix != "" {
		url += "/" + prefix
	}

	req, err := http.NewRequestWithContext(context.Background(), "GET", url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create request %s: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}

	var entries []hfEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	var files []hfEntry
	for _, e := range entries {
		switch e.Type {
		case "file":
			files = append(files, e)
		case "directory":
			subFiles, err := listAllFiles(e.Path)
			if err != nil {
				return nil, err
			}
			files = append(files, subFiles...)
		}
	}

	return files, nil
}

func downloadFile(storageDir, filePath string) error {
	url := fmt.Sprintf("%s/%s/resolve/%s/%s", hfResolve, hfRepo, hfRevision, filePath)
	localPath := filepath.Join(storageDir, filePath)

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), "GET", url, http.NoBody)
	if err != nil {
		return fmt.Errorf("create request %s: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}

	tmpPath := localPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write file: %w", err)
	}
	_ = f.Close()

	if err := os.Rename(tmpPath, localPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}
