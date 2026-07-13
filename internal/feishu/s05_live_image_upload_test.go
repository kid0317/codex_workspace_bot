package feishu_test

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/config"
	"github.com/kid0317/codex-workspace-bot/internal/feishu"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

// TestS05LiveFeishuImageUpload sends one tiny PNG through the production
// image branch. It is opt-in because it intentionally posts to the configured
// test conversation, and it never logs the returned key or message ID.
func TestS05LiveFeishuImageUpload(t *testing.T) {
	if os.Getenv("S05_RUN_LIVE_FEISHU_IMAGE_UPLOAD") != "1" {
		t.Skip("set S05_RUN_LIVE_FEISHU_IMAGE_UPLOAD=1 for the explicit S05 native-image smoke")
	}
	appName := os.Getenv("S05_FEISHU_APP_NAME")
	if appName == "" {
		appName = "aipm"
	}
	replyID := os.Getenv("S05_FEISHU_REPLY_ID")
	if replyID == "" {
		t.Fatal("S05_FEISHU_REPLY_ID is required")
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
	pixel, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScL0jwAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "s05-native-image-*.png")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(pixel); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	assetKey, messageID, err := feishu.NewSender(app.FeishuAppID, app.FeishuAppSecret).UploadAndSend(ctx, worker.ReplyTarget{ID: replyID, Type: "chat_id"}, file, "s05-native-image-smoke.png")
	if err != nil || assetKey == "" || messageID == "" {
		t.Fatalf("native image upload result: asset_key_present=%t message_id_present=%t err=%v", assetKey != "", messageID != "", err)
	}
	t.Log("native image upload and image-message send succeeded")
}
