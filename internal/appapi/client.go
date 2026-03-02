// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package appapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

type Client struct {
	cfg        *Config
	httpClient *http.Client
}

func NewClient(cfg *Config) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	skipCert := os.Getenv("SKIP_CERT_VERIFY")
	if skipCert == "true" || skipCert == "1" {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}
}

func (c *Client) OCSGet(path, userID string) (json.RawMessage, error) {
	url := c.cfg.NextcloudURL + path
	req, err := http.NewRequestWithContext(context.Background(), "GET", url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	c.setHeaders(req, userID)
	req.Header.Set("OCS-APIRequest", "true")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		slog.Warn("OCS request failed", "url", url, "status", resp.StatusCode, "body", string(body))
		return nil, fmt.Errorf("OCS request failed with status %d", resp.StatusCode)
	}

	var ocsResp struct {
		OCS struct {
			Data json.RawMessage `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &ocsResp); err != nil {
		return nil, fmt.Errorf("parsing OCS response: %w", err)
	}

	return ocsResp.OCS.Data, nil
}

func (c *Client) setHeaders(req *http.Request, userID string) {
	req.Header.Set("EX-APP-ID", c.cfg.AppID)
	req.Header.Set("EX-APP-VERSION", c.cfg.AppVersion)
	req.Header.Set("AUTHORIZATION-APP-API", encodeAuth(userID, c.cfg.AppSecret))
	req.Header.Set("Accept", "application/json")
}

func (c *Client) OCSPost(path, userID string, body any) (json.RawMessage, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling body: %w", err)
	}

	url := c.cfg.NextcloudURL + path
	req, err := http.NewRequestWithContext(context.Background(), "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	c.setHeaders(req, userID)
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		slog.Warn("OCS POST request failed", "url", url, "status", resp.StatusCode, "body", string(respBody))
		return nil, fmt.Errorf("OCS POST request failed with status %d", resp.StatusCode)
	}

	var ocsResp struct {
		OCS struct {
			Data json.RawMessage `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(respBody, &ocsResp); err != nil {
		return nil, fmt.Errorf("parsing OCS response: %w", err)
	}

	return ocsResp.OCS.Data, nil
}

func (c *Client) OCSPut(path, userID string, body any) (json.RawMessage, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling body: %w", err)
	}

	url := c.cfg.NextcloudURL + path
	req, err := http.NewRequestWithContext(context.Background(), "PUT", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	c.setHeaders(req, userID)
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		slog.Warn("OCS PUT request failed", "url", url, "status", resp.StatusCode, "body", string(respBody))
		return nil, fmt.Errorf("OCS PUT request failed with status %d", resp.StatusCode)
	}

	var ocsResp struct {
		OCS struct {
			Data json.RawMessage `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(respBody, &ocsResp); err != nil {
		return nil, fmt.Errorf("parsing OCS response: %w", err)
	}

	return ocsResp.OCS.Data, nil
}

// SetInitStatus reports init progress (0-100) back to AppAPI.
// 100 means init complete and triggers auto-enable.
func (c *Client) SetInitStatus(progress int) error {
	path := fmt.Sprintf("/ocs/v1.php/apps/app_api/apps/status/%s", c.cfg.AppID)
	_, err := c.OCSPut(path, "", map[string]any{
		"progress": progress,
		"error":    "",
	})
	if err != nil {
		return fmt.Errorf("setting init status: %w", err)
	}
	slog.Info("init status reported", "progress", progress)
	return nil
}

func encodeAuth(username, secret string) string {
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + secret))
}
