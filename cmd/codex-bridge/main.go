package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/codexbridge"
)

func main() {
	listenAddr := envOrDefault("CODEX_BRIDGE_LISTEN", "0.0.0.0:7070")
	healthAddr := envOrDefault("CODEX_BRIDGE_HEALTH_LISTEN", "0.0.0.0:7071")
	command := envOrDefault("CODEX_BRIDGE_COMMAND", "codex")
	args := strings.Fields(envOrDefault("CODEX_BRIDGE_ARGS", "app-server --stdio"))

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		slog.Error("codex_bridge_listen_failed", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	health := &http.Server{
		Addr:              healthAddr,
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) }),
		ReadHeaderTimeout: 2 * time.Second,
	}
	go func() {
		if err := health.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("codex_bridge_health_failed", "error", err)
			stop()
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = health.Shutdown(shutdownCtx)
	}()

	slog.Info("codex_bridge_listening", "addr", listenAddr)
	bridge := codexbridge.New(codexbridge.Config{Command: command, Args: args, Env: os.Environ()})
	if err := bridge.Serve(ctx, listener); err != nil {
		slog.Error("codex_bridge_stopped", "error", err)
		os.Exit(1)
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
