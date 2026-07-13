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

// TestS05L3RealDynamicToolSurvivesResume is an explicit, no-Feishu protocol
// gate. It proves the installed App Server accepts the S05 doc-read catalog
// and keeps it available after thread/resume; the handler is deliberately
// local, so this test never reads a real document.
func TestS05L3RealDynamicToolSurvivesResume(t *testing.T) {
	if os.Getenv("S05_RUN_REAL_APP_SERVER") != "1" {
		t.Skip("set S05_RUN_REAL_APP_SERVER=1 for the explicit S05 dynamic-tool L3 smoke")
	}
	dir := t.TempDir()
	runtime := codexapp.NewRuntime(codexapp.Config{Command: "codex", RPCTimeout: 30 * time.Second, TurnTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, Grace: 10 * time.Second, Debug: true, DebugDir: dir})
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("start real app server: %v", err)
	}
	store := &memoryThreadStore{}
	called := make(chan codexapp.ToolCall, 2)
	processor := codexapp.Processor{
		Runtime: runtime,
		Store:   store,
		ToolHandlers: func(worker.Batch) codexapp.ToolHandler {
			return func(_ context.Context, call codexapp.ToolCall) (codexapp.ToolResult, error) {
				called <- call
				return codexapp.ToolResult{Success: true, ContentItems: []codexapp.ToolContentItem{{Type: "inputText", Text: `{"outcome":"sent"}`}}}, nil
			}
		},
	}
	for turn := 1; turn <= 2; turn++ {
		batch := worker.Batch{Runtime: worker.AppRuntime{ID: "s05-l3", WorkspaceDir: filepath.Clean("/root/aipm-codex"), Effort: "low"}, Key: worker.P2PKey("s05-l3", "s05-l3"), Messages: []worker.Message{{ID: "s05-l3-message", ChatGroupID: "s05-l3-group", Query: "Use the feishu.doc_read tool now. Pass document_url exactly as https://example.feishu.cn/docx/EYD9dU6nRo1qG9xVLpmcsnLunye. Do not only describe this action."}}}
		if _, err := processor.Process(context.Background(), batch); err != nil {
			t.Fatalf("process turn %d: %v", turn, err)
		}
		select {
		case call := <-called:
			if call.Tool != "doc_read" || call.ThreadID == "" || call.TurnID == "" || call.CallID == "" {
				t.Fatalf("turn %d tool call=%#v", turn, call)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("turn %d did not call the registered dynamic tool", turn)
		}
	}
	if thread, _ := store.GetChatGroupThread(context.Background(), "s05-l3-group"); thread == "" {
		t.Fatal("thread was not persisted for resume")
	}
}
