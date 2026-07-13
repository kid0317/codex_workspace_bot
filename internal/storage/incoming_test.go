package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
)

func TestPersistIncomingStoresRedactedGoalReceiptAndReceiptTime(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	receivedAt := time.Date(2026, 7, 13, 8, 30, 0, 123000000, time.UTC)
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO chat_groups").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT id FROM chat_groups").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("cg-1"))
	mock.ExpectExec("INSERT INTO messages").WithArgs(
		"m-goal", "12345678901234567890123456789012", "cg-1", "e-goal", "om-goal", "ou-1", "/goal [redacted]", receivedAt,
		"goal", "digest", 17, "pending", "pending",
	).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	_, duplicate, err := (&storage.Store{DB: db}).PersistIncoming(context.Background(), "app-1", "group", "oc-1", storage.MessageInput{
		ID: "m-goal", TraceID: "12345678901234567890123456789012", FeishuEventID: "e-goal", FeishuUserMessageID: "om-goal", SenderOpenID: "ou-1",
		UserContent: "/goal [redacted]", ReceivedAt: receivedAt, CommandKind: "goal", CommandPayloadSHA256: "digest", CommandPayloadBytes: 17,
	})
	if err != nil || duplicate {
		t.Fatalf("PersistIncoming() duplicate=%v err=%v", duplicate, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPersistIncomingCommitsChatGroupAndMessageTogether(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO chat_groups").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT id FROM chat_groups").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("cg-1"))
	mock.ExpectExec("INSERT INTO messages").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	_, duplicate, err := (&storage.Store{DB: db}).PersistIncoming(context.Background(), "app-1", "group", "oc-1", storage.MessageInput{ID: "m-1", TraceID: "12345678901234567890123456789012", FeishuEventID: "e-1", FeishuUserMessageID: "om-1", UserContent: "x"})
	if err != nil || duplicate {
		t.Fatalf("PersistIncoming() duplicate=%v err=%v", duplicate, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPersistIncomingStagesAttachmentsInSameTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO chat_groups").WithArgs("app-1", "group", "oc-1").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT id FROM chat_groups").WithArgs("app-1", "group", "oc-1").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("cg-1"))
	mock.ExpectExec("INSERT INTO messages").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO attachments").WithArgs("attachment-1", "m-1", "cg-1", "image", []byte("ciphertext"), 1, "om-1", "photo.png", "image/png").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	_, duplicate, err := (&storage.Store{DB: db}).PersistIncoming(context.Background(), "app-1", "group", "oc-1", storage.MessageInput{
		ID: "m-1", TraceID: "12345678901234567890123456789012", FeishuEventID: "e-1", FeishuUserMessageID: "om-1", UserContent: "附件消息",
		Attachments: []storage.AttachmentInput{{ID: "attachment-1", Kind: storage.AttachmentImage, SourceResourceRefEnc: []byte("ciphertext"), SourceRefKeyVersion: 1, SourceMessageID: "om-1", OriginalNameSafe: "photo.png", DeclaredMIME: "image/png"}},
	})
	if err != nil || duplicate {
		t.Fatalf("PersistIncoming() duplicate=%v err=%v", duplicate, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
