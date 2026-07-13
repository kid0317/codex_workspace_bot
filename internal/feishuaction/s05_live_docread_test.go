package feishuaction_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/codexapp"
	"github.com/kid0317/codex-workspace-bot/internal/config"
	"github.com/kid0317/codex-workspace-bot/internal/feishu"
	"github.com/kid0317/codex-workspace-bot/internal/feishuaction"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

// TestS05LiveFeishuDocumentRead is an explicit L4 gate for the exact
// document-read proxy path. It never logs document contents and requires a
// document that the selected App can already read.
func TestS05LiveFeishuDocumentRead(t *testing.T) {
	if os.Getenv("S05_RUN_LIVE_FEISHU_DOC_READ") != "1" {
		t.Skip("set S05_RUN_LIVE_FEISHU_DOC_READ=1 for the explicit S05 Feishu document-read smoke")
	}
	documentURL := os.Getenv("S05_FEISHU_DOC_URL")
	if documentURL == "" {
		t.Fatal("S05_FEISHU_DOC_URL is required")
	}
	appName := os.Getenv("S05_FEISHU_APP_NAME")
	if appName == "" {
		appName = "aipm"
	}
	cfg, err := config.Load(filepath.Join("..", "..", "config.yaml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	store, err := storage.Open(context.Background(), cfg.Database.DSN())
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	defer store.Close()
	app, err := store.FindAppByName(context.Background(), appName)
	if err != nil {
		t.Fatalf("find app %q: %v", appName, err)
	}
	arguments, err := json.Marshal(map[string]string{"document_url": documentURL})
	if err != nil {
		t.Fatal(err)
	}
	service := feishuaction.Service{Clients: map[string]feishuaction.Client{
		app.ID: feishu.NewSender(app.FeishuAppID, app.FeishuAppSecret),
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := service.Execute(ctx, feishuaction.Route{AppID: app.ID, Reply: worker.ReplyTarget{ID: "l4-read-only", Type: "chat_id"}}, codexapp.ToolCall{Tool: "doc_read", Arguments: arguments})
	if err != nil || !result.Success || len(result.ContentItems) != 1 {
		t.Fatalf("read document result=%#v err=%v", result.Success, err)
	}
	t.Logf("read document through current App authorization: bytes=%d", len(result.ContentItems[0].Text))
}
