package storage_test

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/kid0317/codex-workspace-bot/internal/router"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
)

func TestPrepareCatalogUpgradePersistsArchivePendingBeforeExternalArchive(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &storage.Store{DB: db}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT codex_thread_id,codex_tool_catalog_version,catalog_upgrade_state,catalog_upgrade_from_thread_id,catalog_upgrade_target FROM chat_groups WHERE id=\\? FOR UPDATE").
		WithArgs("group-1").
		WillReturnRows(sqlmock.NewRows([]string{"codex_thread_id", "codex_tool_catalog_version", "catalog_upgrade_state", "catalog_upgrade_from_thread_id", "catalog_upgrade_target"}).AddRow("thread-old", "s05-feishu-v2", "stable", nil, nil))
	mock.ExpectExec("UPDATE chat_groups SET catalog_upgrade_state='archive_pending'").
		WithArgs("thread-old", "s06-schedule-v1", "group-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	state, err := store.PrepareCatalogUpgrade(context.Background(), "group-1", "s06-schedule-v1")
	if err != nil {
		t.Fatalf("PrepareCatalogUpgrade() error = %v", err)
	}
	if state.State != storage.CatalogUpgradeArchivePending || state.FromThreadID != "thread-old" || state.Target != "s06-schedule-v1" || state.CurrentVersion != "s05-feishu-v2" {
		t.Fatalf("PrepareCatalogUpgrade() = %#v", state)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogUpgradeAdvanceAndCompleteUseConditionalTransitions(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &storage.Store{DB: db}
	mock.ExpectExec("UPDATE chat_groups SET codex_thread_id=NULL,catalog_upgrade_state='start_pending'").
		WithArgs("group-1", "thread-old").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE chat_groups SET codex_thread_id=\\?,codex_toolset_version=\\?,codex_tool_catalog_version=\\?,catalog_upgrade_state='stable'").
		WithArgs("thread-new", "s06-schedule-v1", "s06-schedule-v1", "group-1", "s06-schedule-v1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	advanced, err := store.AdvanceCatalogUpgradeAfterArchive(context.Background(), "group-1", "thread-old")
	if err != nil || !advanced {
		t.Fatalf("AdvanceCatalogUpgradeAfterArchive() advanced=%t err=%v", advanced, err)
	}
	completed, err := store.CompleteCatalogUpgrade(context.Background(), "group-1", "s06-schedule-v1", "thread-new")
	if err != nil || !completed {
		t.Fatalf("CompleteCatalogUpgrade() completed=%t err=%v", completed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateMessageReportsDuplicateEvent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec("INSERT INTO messages").WillReturnError(&mysql.MySQLError{Number: 1062})
	store := &storage.Store{DB: db}

	_, duplicate, err := store.CreateMessage(context.Background(), router.MessageInput{ID: "m1", TraceID: "12345678901234567890123456789012", ChatGroupID: "g1", FeishuEventID: "e1", FeishuUserMessageID: "om1", UserContent: "hello"})
	if err != nil || !duplicate {
		t.Fatalf("CreateMessage() duplicate=%v error=%v", duplicate, err)
	}
}

func TestChatGroupThreadCompareAndSwap(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &storage.Store{DB: db}
	mock.ExpectQuery("SELECT codex_thread_id FROM chat_groups").WithArgs("group-1").WillReturnRows(sqlmock.NewRows([]string{"codex_thread_id"}).AddRow(nil))
	threadID, err := store.GetChatGroupThread(context.Background(), "group-1")
	if err != nil || threadID != "" {
		t.Fatalf("GetChatGroupThread() = %q, %v", threadID, err)
	}
	mock.ExpectExec("UPDATE chat_groups SET codex_thread_id").WithArgs("thread-new", "group-1", nil).WillReturnResult(sqlmock.NewResult(0, 1))
	changed, err := store.SetThreadIfExpected(context.Background(), "group-1", "", "thread-new")
	if err != nil || !changed {
		t.Fatalf("SetThreadIfExpected() = %v, %v", changed, err)
	}
}

func TestCompleteBatchPersistsTurnDuration(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE messages SET status='succeeded'").WithArgs("om_card", "本轮已完成。", int64(1234), "message-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := (&storage.Store{DB: db}).CompleteBatch(context.Background(), []string{"message-1"}, "om_card", "本轮已完成。", 1234); err != nil {
		t.Fatal(err)
	}
}

func TestMarkCompanionDeliveryStartedMarksWholeBatchAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE messages SET companion_delivery_batch_id").WithArgs("batch-1", "message-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE messages SET companion_delivery_batch_id").WithArgs("batch-1", "message-2").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := (&storage.Store{DB: db}).MarkCompanionDeliveryStarted(context.Background(), []string{"message-1", "message-2"}, "batch-1"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFailCompanionDeliveryClearsStageForWholeBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE messages SET status='failed'").WithArgs("companion_delivery_cancelled", "cancelled", int64(12), "batch-1", "message-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE messages SET status='failed'").WithArgs("companion_delivery_cancelled", "cancelled", int64(12), "batch-1", "message-2").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := (&storage.Store{DB: db}).FailCompanionDelivery(context.Background(), []string{"message-1", "message-2"}, "batch-1", "companion_delivery_cancelled", "cancelled", 12); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteCompanionDeliveryFinalizesWholeMarkedBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE messages SET status='succeeded'").WithArgs("om-first", "final text", int64(12), "companion_segment_delivery_partial", "batch-1", "message-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE messages SET status='succeeded'").WithArgs("om-first", "final text", int64(12), "companion_segment_delivery_partial", "batch-1", "message-2").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	summary := storage.CompanionDeliverySummary{BatchID: "batch-1", FirstMessageID: "om-first", Content: "final text", DurationMS: 12, ErrorCode: "companion_segment_delivery_partial"}
	if err := (&storage.Store{DB: db}).CompleteCompanionDelivery(context.Background(), []string{"message-1", "message-2"}, summary); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileAbandonedCompanionDeliveryOnlyTouchesMarkedProcessingBatches(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("SELECT DISTINCT companion_delivery_batch_id FROM messages").WillReturnRows(sqlmock.NewRows([]string{"companion_delivery_batch_id"}).AddRow("batch-1"))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE messages SET status='failed'").WithArgs("batch-1").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	count, err := (&storage.Store{DB: db}).ReconcileAbandonedCompanionDeliveries(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("reconcile = %d, %v", count, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
