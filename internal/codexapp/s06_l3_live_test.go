package codexapp_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/codexapp"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

// TestS06L3RealScheduleToolSurvivesResume verifies the installed App Server
// accepts the complete S06 catalog and routes an exact schedule.create call on
// both a fresh thread and a resumed thread. The handler is local and returns
// only safe metadata, so this never creates a real task or contacts Feishu.
func TestS06L3RealScheduleToolSurvivesResume(t *testing.T) {
	if os.Getenv("S06_RUN_REAL_APP_SERVER") != "1" {
		t.Skip("set S06_RUN_REAL_APP_SERVER=1 for the explicit S06 dynamic-tool L3 smoke")
	}
	runtime := codexapp.NewRuntime(codexapp.Config{Command: "codex", RPCTimeout: 30 * time.Second, TurnTimeout: 3 * time.Minute, IdleTimeout: 90 * time.Second, Grace: 10 * time.Second, Debug: true, DebugDir: t.TempDir()})
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("start real app server: %v", err)
	}
	store := &memoryThreadStore{}
	called := make(chan codexapp.ToolCall, 2)
	processor := codexapp.Processor{
		Runtime:            runtime,
		Store:              store,
		ToolCatalogVersion: codexapp.S06ToolCatalogVersion,
		ToolHandlers: func(worker.Batch) codexapp.ToolHandler {
			return func(_ context.Context, call codexapp.ToolCall) (codexapp.ToolResult, error) {
				called <- call
				return codexapp.ToolResult{Success: true, ContentItems: []codexapp.ToolContentItem{{Type: "inputText", Text: `{"success":true,"task":{"id":"l3-task","version":1}}`}}}, nil
			}
		},
	}
	for turn := 1; turn <= 2; turn++ {
		batch := worker.Batch{Runtime: worker.AppRuntime{ID: "s06-l3", WorkspaceDir: filepath.Clean("/root/codex_workspace_bot"), Effort: "low"}, Key: worker.P2PKey("s06-l3", "s06-l3"), Messages: []worker.Message{{ID: "s06-l3-message", ChatGroupID: "s06-l3-group", Actor: worker.ActorPrincipal{OpenID: "ou-s06-l3"}, Query: "Use the schedule.create tool now. Create a silent prompt task with cron exactly */15 * * * * and prompt exactly L3 safe task. Do not only explain; call the tool."}}}
		if _, err := processor.Process(context.Background(), batch); err != nil {
			t.Fatalf("process turn %d: %v", turn, err)
		}
		select {
		case call := <-called:
			if call.Tool != "create" || call.ThreadID == "" || call.TurnID == "" || call.CallID == "" {
				t.Fatalf("turn %d tool call=%#v", turn, call)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("turn %d did not call schedule.create", turn)
		}
	}
	if thread, _ := store.GetChatGroupThread(context.Background(), "s06-l3-group"); thread == "" {
		t.Fatal("thread was not persisted for resume")
	}
}
