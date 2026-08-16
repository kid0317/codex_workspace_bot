package main

import (
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/providerproxy"
)

func main() {
	upstream, err := url.Parse(os.Getenv("PROVIDER_UPSTREAM_BASE_URL"))
	if err != nil {
		slog.Error("provider_proxy_config_invalid")
		os.Exit(1)
	}
	handler, err := providerproxy.New(providerproxy.Config{
		Upstream: upstream,
		APIKey:   os.Getenv("PROVIDER_API_KEY"),
	})
	if err != nil {
		slog.Error("provider_proxy_config_invalid")
		os.Exit(1)
	}
	listenAddr := os.Getenv("PROVIDER_PROXY_LISTEN")
	if listenAddr == "" {
		listenAddr = "0.0.0.0:8090"
	}
	server := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       30 * time.Second,
	}
	slog.Info("provider_proxy_listening", "addr", listenAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("provider_proxy_stopped", "error", err)
		os.Exit(1)
	}
}
