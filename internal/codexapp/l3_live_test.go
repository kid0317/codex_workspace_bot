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

func TestL3RealAppServerTurn(t *testing.T) {
	if os.Getenv("S03_RUN_REAL_APP_SERVER") != "1" {
		t.Skip("set S03_RUN_REAL_APP_SERVER=1 for explicit local App Server smoke")
	}
	dir := t.TempDir()
	runtime := codexapp.NewRuntime(codexapp.Config{Command: "codex", RPCTimeout: 30 * time.Second, TurnTimeout: 500 * time.Second, IdleTimeout: 90 * time.Second, Grace: 10 * time.Second, Debug: true, DebugDir: dir})
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("start real app server: %v", err)
	}
	store := &memoryThreadStore{}
	batch := worker.Batch{Runtime: worker.AppRuntime{ID: "l3", WorkspaceDir: filepath.Clean("/root/codex_workspace_bot"), Model: "", Effort: "low"}, Key: worker.P2PKey("l3", "l3"), Messages: []worker.Message{{ID: "l3-message", ChatGroupID: "l3-group", Query: "S03 local protocol smoke. Reply with exactly: ok."}}}
	if _, err := (codexapp.Processor{Runtime: runtime, Store: store}).Process(context.Background(), batch); err != nil {
		t.Fatalf("real turn: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 3 {
		t.Fatalf("debug evidence files = %d, want raw/event/outcome", len(entries))
	}
}
