package debugapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/config"
	"github.com/kid0317/codex-workspace-bot/internal/db"
	"github.com/kid0317/codex-workspace-bot/internal/debugapi"
	"github.com/kid0317/codex-workspace-bot/internal/engine"
	"github.com/kid0317/codex-workspace-bot/internal/feishu"
	"github.com/kid0317/codex-workspace-bot/internal/mockengine"
	"github.com/kid0317/codex-workspace-bot/internal/model"
	"github.com/kid0317/codex-workspace-bot/internal/session"
	"github.com/kid0317/codex-workspace-bot/internal/task"
)

func TestDebugDispatchIsLocalSafeAndCreatesSideEffects(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	cfg := config.Config{Server: config.ServerConfig{DebugEnabled: true, DebugToken: "test-token", MaxBodyBytes: 1024}, Apps: []config.AppConfig{{ID: "demo", WorkspaceMode: "work"}}}
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work"})
	srv := debugapi.New(cfg, map[string]*session.Manager{"demo": mgr})

	req := httptest.NewRequest(http.MethodPost, "/debug/dispatch", bytes.NewBufferString(`{"app_id":"demo","chat_id":"oc","sender_id":"ou","message_id":"m1","text":"hi"}`))
	req.Header.Set("X-Debug-Token", "test-token")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	messages, err := store.Messages().All()
	if err != nil || len(messages) != 2 {
		t.Fatalf("messages = %#v err=%v", messages, err)
	}
}

func TestDebugDisabledAndUnknownAppAreRejected(t *testing.T) {
	srv := debugapi.New(config.Config{Server: config.ServerConfig{DebugEnabled: false}}, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/debug/dispatch", bytes.NewBufferString(`{}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled status = %d", rec.Code)
	}

	srv = debugapi.New(config.Config{Server: config.ServerConfig{DebugEnabled: true, DebugToken: "test-token", MaxBodyBytes: 8}}, map[string]*session.Manager{})
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/dispatch", bytes.NewBufferString(`{"app_id":"missing"}`))
	req.Header.Set("X-Debug-Token", "test-token")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge && rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown/oversize status = %d", rec.Code)
	}
}

func TestDebugTaskRunAndApprovalRespondRoutes(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	cfg := config.Config{Server: config.ServerConfig{DebugEnabled: true, DebugToken: "test-token", MaxBodyBytes: 2048}, Apps: []config.AppConfig{{ID: "demo", WorkspaceMode: "work"}}}
	runner := task.NewRunner(store, mockengine.New(), root)
	srv := debugapi.NewWithServices(cfg, debugapi.Services{Stores: map[string]*db.Store{"demo": store}, TaskRunners: map[string]*task.Runner{"demo": runner}})

	taskPath := filepath.Join(root, "system.yaml")
	bodyMap := map[string]any{"app_id": "demo", "task": map[string]any{"id": "demo/system", "app_id": "demo", "name": "System", "prompt": "maintain", "send_output": false, "enabled": true}}
	body, _ := json.Marshal(bodyMap)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/task/run", bytes.NewReader(body))
	req.Header.Set("X-Debug-Token", "test-token")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("task run status = %d body=%s path=%s", rec.Code, rec.Body.String(), taskPath)
	}

	if err := store.Approvals().Save(model.ApprovalRequest{ID: "a1", AppID: "demo", Status: "pending_user"}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/debug/approval/respond", bytes.NewBufferString(`{"app_id":"demo","approval_id":"a1","decision":"allow"}`))
	req.Header.Set("X-Debug-Token", "test-token")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("approval status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDebugEndpointsRejectWrongMethodOversizeAndCrossAppTask(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	cfg := config.Config{Server: config.ServerConfig{DebugEnabled: true, DebugToken: "test-token", MaxBodyBytes: 32}, Apps: []config.AppConfig{{ID: "demo", WorkspaceMode: "work"}}}
	runner := task.NewRunner(store, mockengine.New(), root)
	srv := debugapi.NewWithServices(cfg, debugapi.Services{TaskRunners: map[string]*task.Runner{"demo": runner}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/debug/task/run", nil)
	req.Header.Set("X-Debug-Token", "test-token")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET task/run status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/debug/task/run", bytes.NewBufferString(`{"app_id":"demo","task":{"id":"other/system","app_id":"other","send_output":false}}`))
	req.Header.Set("X-Debug-Token", "test-token")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge && rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-app/oversize status = %d body=%s", rec.Code, rec.Body.String())
	}

	cfg.Server.MaxBodyBytes = 2048
	srv = debugapi.NewWithServices(cfg, debugapi.Services{Stores: map[string]*db.Store{"demo": store}, TaskRunners: map[string]*task.Runner{"demo": runner}})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/debug/task/run", bytes.NewBufferString(`{"app_id":"demo","task":{"id":"other/system","app_id":"other","send_output":false}}`))
	req.Header.Set("X-Debug-Token", "test-token")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-app status = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/debug/task/run", bytes.NewBufferString(`{"app_id":"demo","task":{"id":"other","send_output":false,"prompt":"system","enabled":true}}`))
	req.Header.Set("X-Debug-Token", "test-token")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-canonical task id status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDebugApprovalRespondUpdatesPersistedRequest(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err := store.Approvals().Save(model.ApprovalRequest{ID: "a1", AppID: "demo", Status: "pending_user"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Server: config.ServerConfig{DebugEnabled: true, DebugToken: "test-token", MaxBodyBytes: 1024}, Apps: []config.AppConfig{{ID: "demo"}}}
	srv := debugapi.NewWithServices(cfg, debugapi.Services{Stores: map[string]*db.Store{"demo": store}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/approval/respond", bytes.NewBufferString(`{"app_id":"demo","approval_id":"a1","decision":"allow"}`))
	req.Header.Set("X-Debug-Token", "test-token")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got, err := store.Approvals().ByID("a1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "user_allowed" || got.ResolvedAt == nil {
		t.Fatalf("approval after respond = %#v", got)
	}
}

func TestDebugDispatchSupportsGroupThreadAttachmentsAndScenario(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	sender := feishu.NewMockSender()
	engine := &scenarioEngine{}
	mgr := session.NewManager(store, engine, sender, session.Options{WorkspaceMode: "work"})
	cfg := config.Config{Server: config.ServerConfig{DebugEnabled: true, DebugToken: "test-token", MaxBodyBytes: 2048}, Apps: []config.AppConfig{{ID: "demo"}}}
	srv := debugapi.New(cfg, map[string]*session.Manager{"demo": mgr})

	body := `{"app_id":"demo","chat_type":"group","chat_id":"oc_group","thread_id":"thread1","sender_id":"ou","message_id":"m1","text":"hi","scenario":"empty_output","attachments":[{"id":"a1","original_name":"a.txt"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/dispatch", bytes.NewBufferString(body))
	req.Header.Set("X-Debug-Token", "test-token")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if engine.scenario != "empty_output" {
		t.Fatalf("scenario = %q", engine.scenario)
	}
	if engine.prompt != "hi" {
		t.Fatalf("prompt = %q", engine.prompt)
	}
	channels, _ := store.Channels().Count()
	if channels != 1 {
		t.Fatalf("channels = %d", channels)
	}
}

func TestDebugEngineScenarioEndpointControlsMock(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work"})
	cfg := config.Config{Server: config.ServerConfig{DebugEnabled: true, DebugToken: "test-token", MaxBodyBytes: 2048}, Apps: []config.AppConfig{{ID: "demo"}}}
	srv := debugapi.New(cfg, map[string]*session.Manager{"demo": mgr})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/engine/scenario", bytes.NewBufferString(`{"app_id":"demo","scenario":"empty_output"}`))
	req.Header.Set("X-Debug-Token", "test-token")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusGone {
		t.Fatalf("scenario status = %d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/debug/dispatch", bytes.NewBufferString(`{"app_id":"demo","chat_id":"oc","sender_id":"ou","message_id":"m1","text":"hi","scenario":"empty_output"}`))
	req.Header.Set("X-Debug-Token", "test-token")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("per-request scenario dispatch status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDebugNonLocalBindRequiresToken(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	mgr := session.NewManager(store, mockengine.New(), feishu.NewMockSender(), session.Options{WorkspaceMode: "work"})
	cfg := config.Config{Server: config.ServerConfig{DebugEnabled: true, DebugBind: "0.0.0.0", AllowNonLocalDebugBind: true, MaxBodyBytes: 1024}, Apps: []config.AppConfig{{ID: "demo"}}}
	srv := debugapi.New(cfg, map[string]*session.Manager{"demo": mgr})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/debug/dispatch", bytes.NewBufferString(`{"app_id":"demo"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d", rec.Code)
	}
}

func TestDebugLocalBindRequiresToken(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	mgr := session.NewManager(store, mockengine.New(), feishu.NewMockSender(), session.Options{WorkspaceMode: "work"})
	cfg := config.Config{Server: config.ServerConfig{DebugEnabled: true, DebugBind: "127.0.0.1", DebugToken: "test-token", MaxBodyBytes: 1024}, Apps: []config.AppConfig{{ID: "demo"}}}
	srv := debugapi.New(cfg, map[string]*session.Manager{"demo": mgr})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/debug/dispatch", bytes.NewBufferString(`{"app_id":"demo"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing local token status = %d", rec.Code)
	}
}

func TestDebugApprovalRespondRequiresPendingSameAppAndKnownDecision(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err := store.Approvals().Save(model.ApprovalRequest{ID: "a1", AppID: "demo", Status: "user_allowed"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Server: config.ServerConfig{DebugEnabled: true, DebugToken: "test-token", MaxBodyBytes: 1024}, Apps: []config.AppConfig{{ID: "demo"}}}
	srv := debugapi.NewWithServices(cfg, debugapi.Services{Stores: map[string]*db.Store{"demo": store}})

	for _, body := range []string{
		`{"app_id":"demo","approval_id":"a1","decision":"allow"}`,
		`{"app_id":"demo","approval_id":"missing","decision":"allow"}`,
		`{"app_id":"demo","approval_id":"a1","decision":"{\"raw\":true}"}`,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/debug/approval/respond", bytes.NewBufferString(body))
		req.Header.Set("X-Debug-Token", "test-token")
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusConflict {
			t.Fatalf("body %s status = %d response=%s", body, rec.Code, rec.Body.String())
		}
	}
}

type scenarioEngine struct {
	scenario string
	prompt   string
}

func (e *scenarioEngine) SendTurn(ctx context.Context, req engine.TurnRequest) (engine.EventStream, error) {
	e.scenario = req.Scenario
	e.prompt = req.Prompt
	threadID := "thread-scenario"
	if req.Scenario == "empty_output" {
		return engine.NewSliceStream([]engine.TurnEvent{
			{Type: engine.EventTurnStarted, ThreadID: threadID},
			{Type: engine.EventCompleted, ThreadID: threadID},
		}), nil
	}
	return engine.NewSliceStream([]engine.TurnEvent{
		{Type: engine.EventTurnStarted, ThreadID: threadID},
		{Type: engine.EventDelta, ThreadID: threadID, Text: "ok"},
		{Type: engine.EventCompleted, ThreadID: threadID},
	}), nil
}
