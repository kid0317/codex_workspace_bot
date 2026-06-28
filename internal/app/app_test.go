package app_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/app"
	"github.com/kid0317/codex-workspace-bot/internal/config"
)

func TestBootstrapBuildsRunnableDebugHandlerAndInitializesWorkspace(t *testing.T) {
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces", "demo")
	cfg := config.Config{
		Server: config.ServerConfig{DebugEnabled: true, DebugBind: "127.0.0.1", MaxBodyBytes: 2048},
		Engine: config.EngineConfig{Type: "mock"},
		Apps: []config.AppConfig{{
			ID: "demo", FeishuAppID: "cli_demo", FeishuAppSecret: "EXAMPLE_SECRET_DO_NOT_USE", WorkspaceDir: workspaceDir, WorkspaceMode: "work",
		}},
	}
	rt, err := app.Bootstrap(cfg)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, "AGENTS.md")); err != nil {
		t.Fatalf("workspace bridge missing: %v", err)
	}

	health := httptest.NewRecorder()
	rt.Handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d body=%s", health.Code, health.Body.String())
	}

	body := bytes.NewBufferString(`{"app_id":"demo","chat_id":"oc_demo","sender_id":"ou_demo","message_id":"m1","text":"hello"}`)
	rec := httptest.NewRecorder()
	rt.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/debug/dispatch", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch status = %d body=%s", rec.Code, rec.Body.String())
	}
	messages, err := rt.Apps["demo"].Store.Messages().All()
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want user+assistant", len(messages))
	}
}

func TestBootstrapRejectsUnknownAppAndKeepsDebugLocal(t *testing.T) {
	cfg := config.Config{
		Server: config.ServerConfig{DebugEnabled: true, DebugBind: "0.0.0.0"},
		Engine: config.EngineConfig{Type: "mock"},
		Apps:   []config.AppConfig{{ID: "demo", FeishuAppID: "cli_demo", FeishuAppSecret: "EXAMPLE", WorkspaceDir: t.TempDir()}},
	}
	if _, err := app.Bootstrap(cfg); err == nil {
		t.Fatal("Bootstrap should reject non-local debug bind without explicit opt-in")
	}
}

func TestBootstrapInjectsGuardrailsAndRoutesTasksByApp(t *testing.T) {
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces", "demo")
	cfg := config.Config{
		Server:     config.ServerConfig{DebugEnabled: true, DebugBind: "127.0.0.1", MaxBodyBytes: 4096},
		Engine:     config.EngineConfig{Type: "mock"},
		Session:    config.SessionConfig{QueueSize: 1, WorkerIdleTimeoutMinutes: 1},
		Guardrails: config.GuardrailConfig{MaxMessageBytes: 4, MaxOutputBytes: 1024, MaxEventsPerTurn: 20},
		Apps: []config.AppConfig{{
			ID: "demo", FeishuAppID: "cli_demo", FeishuAppSecret: "EXAMPLE_SECRET_DO_NOT_USE",
			WorkspaceDir: workspaceDir, WorkspaceMode: "work", AllowedChats: []string{"allowed_chat"},
		}},
	}
	rt, err := app.Bootstrap(cfg)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	body := bytes.NewBufferString(`{"app_id":"demo","chat_id":"blocked_chat","sender_id":"ou","message_id":"m1","text":"hi"}`)
	rec := httptest.NewRecorder()
	rt.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/debug/dispatch", body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("blocked chat status = %d body=%s", rec.Code, rec.Body.String())
	}

	taskBody := bytes.NewBufferString(`{"app_id":"demo","task":{"id":"demo/user","target_type":"p2p","target_id":"allowed_chat","send_output":true,"prompt":"hey","enabled":true}}`)
	rec = httptest.NewRecorder()
	rt.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/debug/task/run", taskBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("task status = %d body=%s", rec.Code, rec.Body.String())
	}
	messages, _ := rt.Apps["demo"].Store.Messages().All()
	if len(messages) != 2 {
		t.Fatalf("task messages = %#v", messages)
	}
}
