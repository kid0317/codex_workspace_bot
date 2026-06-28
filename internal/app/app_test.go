package app_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/app"
	"github.com/kid0317/codex-workspace-bot/internal/config"
	"github.com/kid0317/codex-workspace-bot/internal/db"
	"github.com/kid0317/codex-workspace-bot/internal/feishu"
	"github.com/kid0317/codex-workspace-bot/internal/model"
)

func TestBootstrapBuildsRunnableDebugHandlerAndInitializesWorkspace(t *testing.T) {
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces", "demo")
	cfg := config.Config{
		Server: config.ServerConfig{DebugEnabled: true, DebugBind: "127.0.0.1", DebugToken: "test-token", MaxBodyBytes: 2048},
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
	req := httptest.NewRequest(http.MethodPost, "/debug/dispatch", body)
	req.Header.Set("X-Debug-Token", "test-token")
	rt.Handler.ServeHTTP(rec, req)
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
		Server:     config.ServerConfig{DebugEnabled: true, DebugBind: "127.0.0.1", DebugToken: "test-token", MaxBodyBytes: 4096},
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
	req := httptest.NewRequest(http.MethodPost, "/debug/dispatch", body)
	req.Header.Set("X-Debug-Token", "test-token")
	rt.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("blocked chat status = %d body=%s", rec.Code, rec.Body.String())
	}

	taskBody := bytes.NewBufferString(`{"app_id":"demo","task":{"id":"demo/user","target_type":"p2p","target_id":"allowed_chat","send_output":true,"prompt":"hey","enabled":true}}`)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/debug/task/run", taskBody)
	req.Header.Set("X-Debug-Token", "test-token")
	rt.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("task status = %d body=%s", rec.Code, rec.Body.String())
	}
	messages, _ := rt.Apps["demo"].Store.Messages().All()
	if len(messages) != 2 {
		t.Fatalf("task messages = %#v", messages)
	}
}

func TestBootstrapScansTasksAndRuntimeCloseDrainsManagers(t *testing.T) {
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces", "demo")
	tasksDir := filepath.Join(workspaceDir, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "daily.yaml"), []byte(`
name: Daily
cron: "0 9 * * *"
target_type: p2p
target_id: ou_user
send_output: true
prompt: hello
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Server: config.ServerConfig{DebugEnabled: true, DebugBind: "127.0.0.1", DebugToken: "test-token", MaxBodyBytes: 4096},
		Engine: config.EngineConfig{Type: "mock"},
		Apps: []config.AppConfig{{
			ID: "demo", FeishuAppID: "cli_demo", FeishuAppSecret: "EXAMPLE_SECRET_DO_NOT_USE",
			WorkspaceDir: workspaceDir, WorkspaceMode: "work",
		}},
	}
	rt, err := app.Bootstrap(cfg)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if got := rt.Apps["demo"].Scheduler.Tasks(); len(got) != 1 || got[0].ID != "demo/daily" {
		t.Fatalf("scheduled tasks = %#v", got)
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := rt.Apps["demo"].Manager.Dispatch(context.Background(), feishu.IncomingMessage{
		AppID: "demo", ChatType: "p2p", ChatID: "oc", ChannelKey: "p2p:oc:demo",
		SenderID: "ou", MessageID: "after-close", Prompt: "hi", ReceiveID: "ou", ReceiveType: "open_id",
	}); err == nil {
		t.Fatal("dispatch after runtime close should fail")
	}
}

func TestBootstrapIgnoresMalformedTaskYAMLAndKeepsValidTasks(t *testing.T) {
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces", "demo")
	tasksDir := filepath.Join(workspaceDir, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "daily.yaml"), []byte(`
name: Daily
cron: "0 9 * * *"
target_type: p2p
target_id: ou_user
send_output: true
prompt: hello
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "bad.yaml"), []byte("name: [broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Server: config.ServerConfig{DebugEnabled: true, DebugBind: "127.0.0.1", DebugToken: "test-token", MaxBodyBytes: 4096},
		Engine: config.EngineConfig{Type: "mock"},
		Apps: []config.AppConfig{{
			ID: "demo", FeishuAppID: "cli_demo", FeishuAppSecret: "EXAMPLE_SECRET_DO_NOT_USE",
			WorkspaceDir: workspaceDir, WorkspaceMode: "work",
		}},
	}
	rt, err := app.Bootstrap(cfg)
	if err != nil {
		t.Fatalf("Bootstrap() should ignore malformed task YAML and start: %v", err)
	}
	defer rt.Close(context.Background())
	if got := rt.Apps["demo"].Scheduler.Tasks(); len(got) != 1 || got[0].ID != "demo/daily" {
		t.Fatalf("scheduled tasks = %#v", got)
	}
}

func TestBootstrapExpiresStalePendingApprovals(t *testing.T) {
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces", "demo")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(filepath.Join(workspaceDir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Approvals().Save(model.ApprovalRequest{ID: "approval-old", AppID: "demo", Status: "pending_user", ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Server: config.ServerConfig{DebugEnabled: true, DebugBind: "127.0.0.1", DebugToken: "test-token", MaxBodyBytes: 4096},
		Engine: config.EngineConfig{Type: "mock"},
		Apps: []config.AppConfig{{
			ID: "demo", FeishuAppID: "cli_demo", FeishuAppSecret: "EXAMPLE_SECRET_DO_NOT_USE",
			WorkspaceDir: workspaceDir, WorkspaceMode: "work",
		}},
	}
	rt, err := app.Bootstrap(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close(context.Background())
	got, err := rt.Apps["demo"].Store.Approvals().ByID("approval-old")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "expired" || got.ResolvedAt == nil {
		t.Fatalf("stale approval after bootstrap = %#v", got)
	}
}

func TestBootstrapRejectsUnimplementedCodexAppServerEngine(t *testing.T) {
	cfg := config.Config{
		Server: config.ServerConfig{DebugEnabled: false},
		Engine: config.EngineConfig{Type: "codex-app-server"},
		Apps: []config.AppConfig{{
			ID: "demo", FeishuAppID: "cli_demo", FeishuAppSecret: "EXAMPLE_SECRET_DO_NOT_USE", WorkspaceDir: t.TempDir(), WorkspaceMode: "work",
		}},
	}
	if _, err := app.Bootstrap(cfg); err == nil {
		t.Fatal("Bootstrap should reject codex-app-server until the real runtime is implemented")
	}
}
