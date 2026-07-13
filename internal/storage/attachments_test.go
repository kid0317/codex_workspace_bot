package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
)

func TestClaimAttachmentRequiresStagedState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lease := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	mock.ExpectExec("UPDATE attachments SET state='processing'").WithArgs("attempt-1", lease, "attachment-1").WillReturnResult(sqlmock.NewResult(0, 1))
	claimed, err := (&storage.Store{DB: db}).ClaimAttachment(context.Background(), "attachment-1", "attempt-1", lease)
	if err != nil || !claimed {
		t.Fatalf("ClaimAttachment() claimed=%v err=%v", claimed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteAttachmentRequiresOwningProcessingAttempt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	deadline := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	mock.ExpectExec("UPDATE attachments SET state='ready'").WithArgs("application/pdf", int64(42), "abc123", "session-1", "safe/payload", deadline, "attachment-1", "attempt-1").WillReturnResult(sqlmock.NewResult(0, 1))
	err = (&storage.Store{DB: db}).CompleteAttachment(context.Background(), storage.AttachmentCompletion{ID: "attachment-1", AttemptID: "attempt-1", ObservedMIME: "application/pdf", ByteSize: 42, SHA256: "abc123", SessionID: "session-1", RelativePath: "safe/payload", RetentionDeadline: deadline})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetAttachmentForWorkerReturnsOnlyStagedResourceMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("SELECT id, kind, source_resource_ref_enc").WithArgs("attachment-1").WillReturnRows(sqlmock.NewRows([]string{"id", "kind", "source_resource_ref_enc", "source_ref_key_version", "source_message_id", "original_name_safe", "declared_mime"}).AddRow("attachment-1", "file", []byte("ciphertext"), 1, "om-1", "report.pdf", "application/pdf"))
	record, err := (&storage.Store{DB: db}).GetAttachmentForWorker(context.Background(), "attachment-1")
	if err != nil || record.ID != "attachment-1" || record.Kind != storage.AttachmentFile || record.SourceMessageID != "om-1" {
		t.Fatalf("record=%#v err=%v", record, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimActionCreatesOneClaimedLedgerRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	call := storage.ActionCall{ID: "action-1", AppID: "app-1", ChannelKey: "group:oc-1:app-1", ChatGroupID: "group-1", ThreadID: "thread-1", TurnID: "turn-1", CallID: "call-1", Tool: "feishu.message_send_current_channel", ArgumentsDigest: "digest"}
	mock.ExpectExec("INSERT INTO feishu_action_calls").WithArgs(call.ID, call.AppID, call.ChannelKey, call.ChatGroupID, call.ThreadID, call.TurnID, call.CallID, call.Tool, call.ArgumentsDigest).WillReturnResult(sqlmock.NewResult(1, 1))
	result, err := (&storage.Store{DB: db}).ClaimAction(context.Background(), call)
	if err != nil || result != storage.ActionClaimed {
		t.Fatalf("ClaimAction() result=%q err=%v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStartActionRequiresMatchingClaimedCall(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec("UPDATE feishu_action_calls SET state='in_flight'").WithArgs("app-1", "thread-1", "turn-1", "call-1").WillReturnResult(sqlmock.NewResult(0, 1))
	started, err := (&storage.Store{DB: db}).StartAction(context.Background(), "app-1", "thread-1", "turn-1", "call-1")
	if err != nil || !started {
		t.Fatalf("StartAction() started=%v err=%v", started, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileInterruptedAttachmentsFailsStagedAndProcessingRowsOnRestart(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	deadline := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	mock.ExpectExec("UPDATE attachments SET state='failed'").WithArgs(deadline).WillReturnResult(sqlmock.NewResult(0, 2))
	count, err := (&storage.Store{DB: db}).ReconcileInterruptedAttachments(context.Background(), deadline)
	if err != nil || count != 2 {
		t.Fatalf("reconcile=%d err=%v", count, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListExpiredAttachmentsReturnsOnlyRetentionCleanupProjection(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("SELECT a.id, a.state, apps.workspace_dir").WithArgs(10).WillReturnRows(sqlmock.NewRows([]string{"id", "state", "workspace_dir", "relative_path"}).AddRow("attachment-1", "ready", "/tmp/workspace", ".codex-workspace-bot/attachments/a/payload"))
	records, err := (&storage.Store{DB: db}).ListExpiredAttachments(context.Background(), 10)
	if err != nil || len(records) != 1 || records[0].ID != "attachment-1" || records[0].WorkspaceDir != "/tmp/workspace" {
		t.Fatalf("records=%#v err=%v", records, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteActionRequiresMatchingClaimedOrInFlightCall(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec("UPDATE feishu_action_calls SET state=\\?").WithArgs(storage.ActionSucceeded, []byte("encrypted-result"), 1, "object-digest", "app-1", "thread-1", "turn-1", "call-1").WillReturnResult(sqlmock.NewResult(0, 1))
	err = (&storage.Store{DB: db}).CompleteAction(context.Background(), storage.ActionResult{AppID: "app-1", ThreadID: "thread-1", TurnID: "turn-1", CallID: "call-1", ResultEnc: []byte("encrypted-result"), ResultKeyVersion: 1, ObjectDigest: "object-digest"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetCompletedActionReturnsEncryptedReplayForSameCall(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("SELECT tool, arguments_digest, result_enc, result_key_version, state").WithArgs("app-1", "thread-1", "turn-1", "call-1").WillReturnRows(sqlmock.NewRows([]string{"tool", "arguments_digest", "result_enc", "result_key_version", "state"}).AddRow("feishu.message_send_current_channel", "digest", []byte("ciphertext"), 1, "succeeded"))
	replay, found, err := (&storage.Store{DB: db}).GetCompletedAction(context.Background(), "app-1", "thread-1", "turn-1", "call-1")
	if err != nil || !found || replay.Tool != "feishu.message_send_current_channel" || replay.ResultKeyVersion != 1 {
		t.Fatalf("replay=%#v found=%v err=%v", replay, found, err)
	}
}
