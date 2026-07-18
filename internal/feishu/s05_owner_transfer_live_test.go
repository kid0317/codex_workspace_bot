package feishu

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/config"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

// TestS051L4DocumentOwnerTransfer creates one uniquely titled document for
// the most recent trusted p2p sender and announces it to that same sender.
// It is deliberately opt-in because it makes a visible Feishu document. The
// test never print IDs, secrets, document URLs, or user content.
func TestS051L4DocumentOwnerTransfer(t *testing.T) {
	if os.Getenv("S05_RUN_OWNER_TRANSFER_L4") != "1" {
		t.Skip("set S05_RUN_OWNER_TRANSFER_L4=1 for the explicit real Feishu owner-transfer check")
	}
	configPath := os.Getenv("S05_OWNER_TRANSFER_CONFIG")
	if configPath == "" {
		// go test runs this package with internal/feishu as its working
		// directory; keep the default independent of the caller's shell cwd.
		configPath = filepath.Join("..", "..", "config.yaml")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.FeishuActions.Enabled {
		t.Fatal("feishu actions must be enabled for the owner-transfer check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, cfg.Database.DSN())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	var feishuAppID, feishuAppSecret, senderOpenID string
	err = store.DB.QueryRowContext(ctx, `SELECT a.feishu_app_id, a.feishu_app_secret, m.sender_open_id
		FROM messages m
		JOIN chat_groups cg ON cg.id = m.chat_group_id
		JOIN apps a ON a.id = cg.app_id
		WHERE a.enabled = TRUE AND cg.chat_type = 'p2p' AND m.sender_open_id IS NOT NULL AND m.sender_open_id <> ''
		ORDER BY m.created_at DESC
		LIMIT 1`).Scan(&feishuAppID, &feishuAppSecret, &senderOpenID)
	if err != nil {
		if err == sql.ErrNoRows {
			t.Fatal("no trusted p2p sender is available for this explicit live check")
		}
		t.Fatalf("select trusted sender: %v", err)
	}
	sender := NewSender(feishuAppID, feishuAppSecret, cfg.FeishuActions.DefaultDocFolderToken)
	title := fmt.Sprintf("S05.1 Owner Transfer Verification %d", time.Now().UnixNano())
	outcome, err := sender.CreateDocumentAndAnnounce(ctx, worker.ReplyTarget{ID: senderOpenID, Type: "open_id"}, senderOpenID, title, []byte("# S05.1 Owner Transfer Verification\n\nThis document is an explicit local integration check."))
	if err != nil {
		t.Fatalf("create and announce document: %v", err)
	}
	if outcome.URL == "" || !outcome.ContentWritten || outcome.AnnouncementOutcome != "sent" || !outcome.OwnerTransferred || outcome.OwnerTransferOutcome != "transferred" {
		t.Fatalf("document owner transfer outcome: content_written=%t announcement=%q owner_transferred=%t owner_transfer_outcome=%q", outcome.ContentWritten, outcome.AnnouncementOutcome, outcome.OwnerTransferred, outcome.OwnerTransferOutcome)
	}
}

// TestS051L4GroupDocumentOwnerTransfer is the group counterpart: the URL is
// sent to the group chat, while the Owner is the last trusted group sender.
// It proves those two identities remain distinct at the real Feishu boundary.
func TestS051L4GroupDocumentOwnerTransfer(t *testing.T) {
	if os.Getenv("S05_RUN_OWNER_TRANSFER_L4") != "1" {
		t.Skip("set S05_RUN_OWNER_TRANSFER_L4=1 for the explicit real Feishu owner-transfer check")
	}
	configPath := os.Getenv("S05_OWNER_TRANSFER_CONFIG")
	if configPath == "" {
		configPath = filepath.Join("..", "..", "config.yaml")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.FeishuActions.Enabled {
		t.Fatal("feishu actions must be enabled for the owner-transfer check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, cfg.Database.DSN())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	var feishuAppID, feishuAppSecret, chatID, senderOpenID string
	err = store.DB.QueryRowContext(ctx, `SELECT a.feishu_app_id, a.feishu_app_secret, cg.chat_id, m.sender_open_id
		FROM messages m
		JOIN chat_groups cg ON cg.id = m.chat_group_id
		JOIN apps a ON a.id = cg.app_id
		WHERE a.enabled = TRUE AND cg.chat_type = 'group' AND m.sender_open_id IS NOT NULL AND m.sender_open_id <> ''
		ORDER BY m.created_at DESC
		LIMIT 1`).Scan(&feishuAppID, &feishuAppSecret, &chatID, &senderOpenID)
	if err != nil {
		if err == sql.ErrNoRows {
			t.Fatal("no trusted group sender is available for this explicit live check")
		}
		t.Fatalf("select trusted group sender: %v", err)
	}
	sender := NewSender(feishuAppID, feishuAppSecret, cfg.FeishuActions.DefaultDocFolderToken)
	title := fmt.Sprintf("S05.1 Group Owner Transfer Verification %d", time.Now().UnixNano())
	outcome, err := sender.CreateDocumentAndAnnounce(ctx, worker.ReplyTarget{ID: chatID, Type: "chat_id"}, senderOpenID, title, []byte("# S05.1 Group Owner Transfer Verification\n\nThis document is an explicit local integration check."))
	if err != nil {
		t.Fatalf("create and announce group document: %v", err)
	}
	if outcome.URL == "" || !outcome.ContentWritten || outcome.AnnouncementOutcome != "sent" || !outcome.OwnerTransferred || outcome.OwnerTransferOutcome != "transferred" {
		t.Fatalf("group document owner transfer outcome: content_written=%t announcement=%q owner_transferred=%t owner_transfer_outcome=%q", outcome.ContentWritten, outcome.AnnouncementOutcome, outcome.OwnerTransferred, outcome.OwnerTransferOutcome)
	}
}
