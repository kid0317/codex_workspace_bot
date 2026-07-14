package storage_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
)

// TestAttachmentMySQLUnicodeRelativePathLifecycle is opt-in because it needs
// an isolated MySQL schema. It applies migrations, persists a Unicode final
// leaf through CompleteAttachment, then reads it back through the expiry scan.
func TestAttachmentMySQLUnicodeRelativePathLifecycle(t *testing.T) {
	dsn := os.Getenv("ATTACHMENT_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set ATTACHMENT_MYSQL_DSN to run against an isolated attachment schema")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	store := &storage.Store{DB: db}
	if err := store.Migrate(ctx, "../../migrations"); err != nil {
		t.Fatal(err)
	}

	const appID = "00000000-0000-0000-0000-000000000701"
	const chatGroupID = "00000000-0000-0000-0000-000000000702"
	const messageID = "00000000-0000-0000-0000-000000000703"
	const attachmentID = "00000000-0000-0000-0000-000000000704"
	const attemptID = "00000000-0000-0000-0000-000000000705"
	const sessionID = "00000000-0000-0000-0000-000000000706"
	const relativePath = ".codex-workspace-bot/attachments/app/channel/00000000-0000-0000-0000-000000000706/00000000-0000-0000-0000-000000000704/报告.pdf"
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM attachments WHERE id=?", attachmentID)
		_, _ = db.ExecContext(ctx, "DELETE FROM messages WHERE id=?", messageID)
		_, _ = db.ExecContext(ctx, "DELETE FROM chat_groups WHERE id=?", chatGroupID)
		_, _ = db.ExecContext(ctx, "DELETE FROM apps WHERE id=?", appID)
	})

	if _, err := db.ExecContext(ctx, `INSERT INTO apps (id,name,feishu_app_id,feishu_app_secret,workspace_dir,workspace_mode,model,reasoning_effort,enabled) VALUES (?,?,?,?,?,?,?,?,TRUE)`, appID, "attachment-unicode-path", "cli-attachment-unicode-path", "unused", t.TempDir(), "work", "gpt-5", "medium"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO chat_groups (id,app_id,chat_type,chat_id) VALUES (?,?,?,?)`, chatGroupID, appID, "p2p", "ou-attachment-unicode-path"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO messages (id,trace_id,chat_group_id,feishu_event_id,feishu_user_message_id,user_content,status) VALUES (?,?,?,?,?,?,?)`, messageID, "00000000000000000000000000000703", chatGroupID, "event-attachment-unicode-path", "om-attachment-unicode-path", "attachment", "received"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO attachments (id,message_id,chat_group_id,kind,source_message_id,original_name_safe,state,attempt_id) VALUES (?,?,?,?,?,?,?,?)`, attachmentID, messageID, chatGroupID, "file", "om-attachment-unicode-path", "报告.pdf", "processing", attemptID); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteAttachment(ctx, storage.AttachmentCompletion{
		ID: attachmentID, AttemptID: attemptID, ObservedMIME: "application/pdf", ByteSize: 7, SHA256: "abc123", SessionID: sessionID, RelativePath: relativePath, RetentionDeadline: time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	records, err := store.ListExpiredAttachments(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.ID == attachmentID {
			if record.RelativePath != relativePath {
				t.Fatalf("relative path=%q, want %q", record.RelativePath, relativePath)
			}
			return
		}
	}
	t.Fatal("expired attachment was not returned")
}
