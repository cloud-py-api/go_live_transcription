// SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nextcloud/go_live_transcription/internal/appapi"
	"github.com/nextcloud/go_live_transcription/internal/handlers"
	"github.com/nextcloud/go_live_transcription/internal/service"
)

func main() {
	logLevel := slog.LevelInfo
	if os.Getenv("LT_LOG_LEVEL") == "debug" {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})))

	cfg, err := appapi.LoadConfig()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	slog.Info("starting go_live_transcription",
		"app_id", cfg.AppID,
		"app_version", cfg.AppVersion,
		"port", cfg.AppPort,
	)

	client := appapi.NewClient(cfg)
	svc := service.NewApplication(cfg, client)

	h := handlers.NewHandler(cfg, client, svc)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	skipAuth := map[string]bool{
		"/heartbeat": true,
	}
	authedHandler := appapi.AuthMiddleware(cfg, skipAuth, mux)

	srv := &http.Server{
		Handler:      authedHandler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	var ln net.Listener
	if os.Getenv("HP_SHARED_KEY") != "" {
		sockPath := "/tmp/exapp.sock"
		os.Remove(sockPath) // clean up stale socket
		ln, err = net.Listen("unix", sockPath)
		if err != nil {
			slog.Error("failed to listen on unix socket", "path", sockPath, "error", err)
			os.Exit(1)
		}
		slog.Info("HTTP server listening on unix socket", "path", sockPath)
	} else {
		addr := ":" + cfg.AppPort
		ln, err = net.Listen("tcp", addr)
		if err != nil {
			slog.Error("failed to listen on TCP", "addr", addr, "error", err)
			os.Exit(1)
		}
		slog.Info("HTTP server listening on TCP", "addr", addr)
	}

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	svc.Shutdown()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown error", "error", err)
	}

	slog.Info("shutdown complete")
}
