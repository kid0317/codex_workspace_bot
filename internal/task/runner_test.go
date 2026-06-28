package task_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/db"
	"github.com/kid0317/codex-workspace-bot/internal/engine"
	"github.com/kid0317/codex-workspace-bot/internal/feishu"
	"github.com/kid0317/codex-workspace-bot/internal/mockengine"
	"github.com/kid0317/codex-workspace-bot/internal/model"
	"github.com/kid0317/codex-workspace-bot/internal/session"
	"github.com/kid0317/codex-workspace-bot/internal/task"
)

func TestSystemTaskWritesContextWithoutCreatingChannelSession(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	taskFile := filepath.Join(root, "system.yaml")
	if err := os.WriteFile(taskFile, []byte("name: System\nenabled: true\ncron: \"0 * * * *\"\nsend_output: false\nprompt: maintain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := task.LoadYAML(taskFile, "demo")
	if err != nil {
		t.Fatal(err)
	}
	runner := task.NewRunner(store, mockengine.New(), root)
	if err := runner.Run(context.Background(), loaded); err != nil {
		t.Fatal(err)
	}
	if count, _ := store.Channels().Count(); count != 0 {
		t.Fatalf("channel count = %d, want 0", count)
	}
	if count, _ := store.Sessions().Count(); count != 0 {
		t.Fatalf("session count = %d, want 0", count)
	}
	if _, err := os.Stat(filepath.Join(root, "sessions", "_system", "system", "SESSION_CONTEXT.md")); err != nil {
		t.Fatalf("system context missing: %v", err)
	}
}

func TestSystemTaskFailedEngineDoesNotUpdateLastRunAt(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	loaded := mustLoadTask(t, root, "system-fail.yaml", `
name: System Fail
enabled: true
cron: "0 * * * *"
send_output: false
prompt: maintain
`, "demo")
	runner := task.NewRunner(store, &taskEventEngine{events: []engine.TurnEvent{
		{Type: engine.EventTurnStarted, ThreadID: "thread-system"},
		{Type: engine.EventFailed, ThreadID: "thread-system", Error: "mock failure"},
	}}, root)
	if err := runner.Run(context.Background(), loaded); err == nil {
		t.Fatal("system task should return engine terminal failure")
	}
	assertTaskLastRunAtNil(t, store, loaded.ID)
}

func TestSystemTaskMalformedEngineEventsDoNotUpdateLastRunAt(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	loaded := mustLoadTask(t, root, "system-malformed.yaml", `
name: System Malformed
enabled: true
cron: "0 * * * *"
send_output: false
prompt: maintain
`, "demo")
	runner := task.NewRunner(store, &taskEventEngine{events: []engine.TurnEvent{
		{Type: engine.EventTurnStarted, ThreadID: "thread-system"},
	}}, root)
	if err := runner.Run(context.Background(), loaded); err == nil {
		t.Fatal("system task should reject stream without terminal event")
	}
	assertTaskLastRunAtNil(t, store, loaded.ID)
}

func TestUserFacingTaskRoutesThroughTargetChannel(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	sender := feishu.NewMockSender()
	manager := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work", WorkspaceDir: root})
	runner := task.NewRunnerWithManagers(store, mockengine.New(), root, map[string]*session.Manager{"demo": manager})
	loaded := mustLoadTask(t, root, "user.yaml", `
name: User Task
cron: "0 * * * *"
target_type: p2p
target_id: ou_user
send_output: true
prompt: say hi
`, "demo")
	if err := runner.Run(context.Background(), loaded); err != nil {
		t.Fatal(err)
	}
	messages, _ := store.Messages().All()
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	if !sender.HasCallSequence("SendThinking", "UpdateCard") {
		t.Fatalf("sender calls = %#v", sender.Calls())
	}
}

func TestUserFacingTaskRunsRepeatedlyWithoutMessageDedup(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	sender := feishu.NewMockSender()
	manager := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work", WorkspaceDir: root})
	runner := task.NewRunnerWithManagers(store, mockengine.New(), root, map[string]*session.Manager{"demo": manager})
	loaded := mustLoadTask(t, root, "repeat.yaml", `
name: Repeat Task
cron: "0 * * * *"
target_type: p2p
target_id: ou_user
send_output: true
prompt: say hi
`, "demo")
	if err := runner.Run(context.Background(), loaded); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), loaded); err != nil {
		t.Fatal(err)
	}
	messages, _ := store.Messages().All()
	if len(messages) != 4 {
		t.Fatalf("repeated task runs wrote %d messages, want 4 user+assistant messages: %#v", len(messages), messages)
	}
}

func TestUserFacingTaskWithoutManagerReturnsError(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	runner := task.NewRunnerWithManagers(store, mockengine.New(), root, map[string]*session.Manager{})
	loaded := mustLoadTask(t, root, "missing-manager.yaml", `
name: Missing Manager
cron: "0 * * * *"
target_type: p2p
target_id: ou_user
send_output: true
prompt: say hi
`, "demo")
	if err := runner.Run(context.Background(), loaded); err == nil {
		t.Fatal("user-facing task without a manager should return an error")
	}
}

func TestBorrowChannelTaskPostArchiveArchivesAfterSuccess(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	sender := feishu.NewMockSender()
	manager := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work", WorkspaceDir: root})
	channelKey := "group:oc_group:demo"
	if err := manager.Dispatch(context.Background(), feishu.IncomingMessage{AppID: "demo", ChatType: "group", ChatID: "oc_group", ChannelKey: channelKey, SenderID: "ou", MessageID: "m1", Prompt: "start", ReceiveID: "oc_group", ReceiveType: "chat_id"}); err != nil {
		t.Fatal(err)
	}
	runner := task.NewRunnerWithManagers(store, mockengine.New(), root, map[string]*session.Manager{"demo": manager})
	loaded := mustLoadTask(t, root, "borrow.yaml", `
name: Borrow Task
cron: "0 * * * *"
target_type: group
target_id: oc_group
send_output: false
post_archive: true
prompt: maintain
`, "demo")
	if err := runner.Run(context.Background(), loaded); err != nil {
		t.Fatal(err)
	}
	sessions, _ := store.Sessions().ByChannel(channelKey)
	if len(sessions) != 1 || sessions[0].Status != "archived" {
		t.Fatalf("sessions = %#v", sessions)
	}
	if len(sender.Calls()) != 2 {
		t.Fatalf("borrow-channel should not send task output, sender calls = %#v", sender.Calls())
	}
}

func mustLoadTask(t *testing.T, root, name, body, appID string) model.Task {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := task.LoadYAML(path, appID)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func assertTaskLastRunAtNil(t *testing.T, store *db.Store, id string) {
	t.Helper()
	tasks, err := store.Tasks().All()
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range tasks {
		if got.ID == id && got.LastRunAt != nil {
			t.Fatalf("task %s LastRunAt = %v, want nil", id, got.LastRunAt)
		}
	}
}

type taskEventEngine struct {
	events []engine.TurnEvent
}

func (e *taskEventEngine) SendTurn(ctx context.Context, req engine.TurnRequest) (engine.EventStream, error) {
	return engine.NewSliceStream(e.events), nil
}
