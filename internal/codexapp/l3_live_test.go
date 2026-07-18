package codexapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// TestS07L3RealGoalProjectsVisibleOutput is an explicit, no-Feishu acceptance
// gate for the two user-visible Goal regressions: the objective must start
// immediately and yield an allowlisted item that can drive both card progress
// or final content before the authoritative Goal terminal status closes it.
func TestS07L3RealGoalProjectsVisibleOutput(t *testing.T) {
	if os.Getenv("S07_RUN_REAL_APP_SERVER") != "1" {
		t.Skip("set S07_RUN_REAL_APP_SERVER=1 for the explicit S07 Goal L3 smoke")
	}
	workspace := t.TempDir()
	nonce := fmt.Sprintf("s07-goal-%d", time.Now().UnixNano())
	target := filepath.Join(workspace, nonce+".txt")
	objective := fmt.Sprintf("Create the file %q containing exactly %q, read it back to verify the exact content, then give a concise final verification.", target, nonce)
	runtime := codexapp.NewRuntime(codexapp.Config{Command: "codex", RPCTimeout: 30 * time.Second, TurnTimeout: 8 * time.Minute, IdleTimeout: 90 * time.Second, Grace: 10 * time.Second, Debug: true, DebugDir: t.TempDir()})
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("start real app server: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	threadResult, err := runtime.Call(ctx, "thread/start", codexapp.ThreadStartParams{CWD: workspace, ApprovalPolicy: "never", Sandbox: "workspace-write"})
	if err != nil {
		t.Fatalf("start Goal thread: %v", err)
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(threadResult, &started); err != nil || started.Thread.ID == "" {
		t.Fatalf("thread/start result = %s, err=%v", threadResult, err)
	}
	defer func() {
		archiveCtx, archiveCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer archiveCancel()
		_, _ = runtime.Call(archiveCtx, "thread/archive", map[string]string{"threadId": started.Thread.ID})
	}()
	var visible []codexapp.CompletedItem
	goal, err := runtime.PrepareGoal(ctx, started.Thread.ID, codexapp.TurnStartParams{CWD: workspace, ApprovalPolicy: "never", ClientUserMessageID: nonce, Input: []codexapp.TextInput{{Type: "text", Text: objective}}, OnItem: func(item codexapp.CompletedItem) bool {
		visible = append(visible, item)
		return true
	}})
	if err != nil {
		t.Fatalf("prepare Goal: %v", err)
	}
	defer goal.Close()
	if _, err := runtime.Call(ctx, "thread/goal/set", map[string]any{"threadId": started.Thread.ID, "objective": objective, "status": "active"}); err != nil {
		t.Fatalf("set Goal active: %v", err)
	}
	if _, err := goal.Start(ctx); err != nil {
		t.Fatalf("run Goal: %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != nonce {
		t.Fatalf("Goal file content = %q, err=%v, want %q", content, err, nonce)
	}
	if len(visible) == 0 {
		t.Fatal("Goal completed without an allowlisted visible agent item")
	}
	for _, item := range visible {
		if item.Type != "agentMessage" || (item.Phase != "commentary" && item.Phase != "final_answer") || strings.TrimSpace(item.Text) == "" {
			t.Fatalf("unexpected visible item: %#v", item)
		}
	}
	goalResult, err := runtime.Call(ctx, "thread/goal/get", map[string]string{"threadId": started.Thread.ID})
	if err != nil || !strings.Contains(string(goalResult), `"complete"`) {
		t.Fatalf("Goal status = %s, err=%v", goalResult, err)
	}
}
