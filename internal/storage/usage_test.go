package storage_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kid0317/codex-workspace-bot/internal/observability"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
)

func TestRecordTurnUsageAtomicallyAccumulatesSessionOnlyForFirstTraceTurn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &storage.Store{DB: db}
	record := observability.TurnUsageRecord{
		TraceID: "0123456789abcdef0123456789abcdef", ThreadID: "thread-1", TurnID: "turn-1",
		Session: observability.SessionUsageKey{AppID: "app-1", ChatType: "p2p", ChatID: "oc-1"},
		Usage:   observability.Usage{InputTokens: 100, OutputTokens: 30, CachedInputTokens: 60, ReasoningOutputTokens: 10, TotalTokens: 130},
	}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT IGNORE INTO turn_usage_ledger").
		WithArgs(record.TraceID, record.ThreadID, record.TurnID, int64(100), int64(30), int64(60), int64(10), int64(130)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO thread_usage_snapshots").
		WithArgs("app-1", "p2p", "oc-1", record.ThreadID, record.TraceID, record.TurnID, int64(100), int64(30), int64(60), int64(10), int64(130)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT IGNORE INTO session_usage_totals").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT completed_turn_count FROM session_usage_totals").
		WithArgs("app-1", "p2p", "oc-1").
		WillReturnRows(sqlmock.NewRows([]string{"completed_turn_count"}).AddRow(0))
	mock.ExpectQuery("SELECT COALESCE\\(SUM\\(input_tokens\\),0\\)").
		WithArgs("app-1", "p2p", "oc-1").
		WillReturnRows(sqlmock.NewRows([]string{"input_tokens", "output_tokens", "cached_input_tokens", "reasoning_output_tokens", "total_tokens"}).AddRow(100, 30, 60, 10, 130))
	mock.ExpectExec("UPDATE session_usage_totals").
		WithArgs(int64(100), int64(30), int64(60), int64(10), int64(130), int64(1), record.TraceID, record.ThreadID, record.TurnID, "app-1", "p2p", "oc-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	total, inserted, err := store.RecordTurnUsage(context.Background(), record)
	if err != nil || !inserted {
		t.Fatalf("RecordTurnUsage() inserted=%t err=%v", inserted, err)
	}
	if total.TotalTokens != 130 || total.CompletedTurnCount != 1 {
		t.Fatalf("RecordTurnUsage() total=%#v", total)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordTurnUsageDuplicateDoesNotIncrementSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &storage.Store{DB: db}
	record := observability.TurnUsageRecord{
		TraceID: "0123456789abcdef0123456789abcdef", ThreadID: "thread-1", TurnID: "turn-1",
		Session: observability.SessionUsageKey{AppID: "app-1", ChatType: "p2p", ChatID: "oc-1"},
		Usage:   observability.Usage{InputTokens: 100, OutputTokens: 30, CachedInputTokens: 60, ReasoningOutputTokens: 10, TotalTokens: 130},
	}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT IGNORE INTO turn_usage_ledger").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT IGNORE INTO session_usage_totals").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT completed_turn_count FROM session_usage_totals").
		WithArgs("app-1", "p2p", "oc-1").
		WillReturnRows(sqlmock.NewRows([]string{"completed_turn_count"}).AddRow(1))
	mock.ExpectQuery("SELECT COALESCE\\(SUM\\(input_tokens\\),0\\)").
		WithArgs("app-1", "p2p", "oc-1").
		WillReturnRows(sqlmock.NewRows([]string{"input_tokens", "output_tokens", "cached_input_tokens", "reasoning_output_tokens", "total_tokens"}).AddRow(100, 30, 60, 10, 130))
	mock.ExpectExec("UPDATE session_usage_totals").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	total, inserted, err := store.RecordTurnUsage(context.Background(), record)
	if err != nil || inserted {
		t.Fatalf("RecordTurnUsage() inserted=%t err=%v", inserted, err)
	}
	if total.CompletedTurnCount != 1 {
		t.Fatalf("RecordTurnUsage() total=%#v", total)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordThreadUsageSnapshotReplacesSessionFallbackWithLatestPlaintextCounters(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &storage.Store{DB: db}
	record := observability.ThreadUsageSnapshotRecord{TraceID: "0123456789abcdef0123456789abcdef", ThreadID: "thread-1", TurnID: "turn-1", Session: observability.SessionUsageKey{AppID: "app-1", ChatType: "p2p", ChatID: "oc-1"}, Usage: observability.Usage{InputTokens: 120, OutputTokens: 30, CachedInputTokens: 70, ReasoningOutputTokens: 8, TotalTokens: 150}}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT input_tokens,output_tokens,cached_input_tokens,reasoning_output_tokens,total_tokens FROM thread_usage_snapshots").
		WithArgs("app-1", "p2p", "oc-1", "thread-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO thread_usage_snapshots").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT IGNORE INTO session_usage_totals").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT completed_turn_count FROM session_usage_totals").WithArgs("app-1", "p2p", "oc-1").WillReturnRows(sqlmock.NewRows([]string{"completed_turn_count"}).AddRow(0))
	mock.ExpectQuery("SELECT COALESCE\\(SUM\\(input_tokens\\),0\\)").WithArgs("app-1", "p2p", "oc-1").WillReturnRows(sqlmock.NewRows([]string{"input_tokens", "output_tokens", "cached_input_tokens", "reasoning_output_tokens", "total_tokens"}).AddRow(120, 30, 70, 8, 150))
	mock.ExpectExec("UPDATE session_usage_totals").WithArgs(int64(120), int64(30), int64(70), int64(8), int64(150), int64(0), record.TraceID, record.ThreadID, record.TurnID, "app-1", "p2p", "oc-1").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	total, err := store.RecordThreadUsageSnapshot(context.Background(), record)
	if err != nil || total.TotalTokens != 150 {
		t.Fatalf("RecordThreadUsageSnapshot() total=%#v err=%v", total, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUsageTotalsKeepAuthoritativeThreadsWhenAnotherThreadFallsBackToSnapshots(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &storage.Store{DB: db}
	session := observability.SessionUsageKey{AppID: "app-1", ChatType: "p2p", ChatID: "oc-1"}
	authoritative := observability.TurnUsageRecord{TraceID: "trace-a", ThreadID: "thread-a", TurnID: "turn-a", Session: session, Usage: observability.Usage{InputTokens: 80, OutputTokens: 20, CachedInputTokens: 40, ReasoningOutputTokens: 5, TotalTokens: 100}}

	mock.ExpectBegin()
	mock.ExpectExec("INSERT IGNORE INTO turn_usage_ledger").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO thread_usage_snapshots").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT IGNORE INTO session_usage_totals").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT completed_turn_count FROM session_usage_totals").WillReturnRows(sqlmock.NewRows([]string{"completed_turn_count"}).AddRow(0))
	mock.ExpectQuery("SELECT COALESCE\\(SUM\\(input_tokens\\),0\\)").WillReturnRows(sqlmock.NewRows([]string{"input_tokens", "output_tokens", "cached_input_tokens", "reasoning_output_tokens", "total_tokens"}).AddRow(80, 20, 40, 5, 100))
	mock.ExpectExec("UPDATE session_usage_totals").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if _, inserted, err := store.RecordTurnUsage(context.Background(), authoritative); err != nil || !inserted {
		t.Fatalf("RecordTurnUsage() inserted=%t err=%v", inserted, err)
	}

	fallback := observability.ThreadUsageSnapshotRecord{TraceID: "trace-b", ThreadID: "thread-b", TurnID: "turn-b", Session: session, Usage: observability.Usage{InputTokens: 104, OutputTokens: 26, CachedInputTokens: 52, ReasoningOutputTokens: 7, TotalTokens: 130}}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT input_tokens,output_tokens,cached_input_tokens,reasoning_output_tokens,total_tokens FROM thread_usage_snapshots").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO thread_usage_snapshots").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT IGNORE INTO session_usage_totals").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT completed_turn_count FROM session_usage_totals").WillReturnRows(sqlmock.NewRows([]string{"completed_turn_count"}).AddRow(1))
	mock.ExpectQuery("SELECT COALESCE\\(SUM\\(input_tokens\\),0\\)").WillReturnRows(sqlmock.NewRows([]string{"input_tokens", "output_tokens", "cached_input_tokens", "reasoning_output_tokens", "total_tokens"}).AddRow(184, 46, 92, 12, 230))
	mock.ExpectExec("UPDATE session_usage_totals").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if total, err := store.RecordThreadUsageSnapshot(context.Background(), fallback); err != nil || total.TotalTokens != 230 || total.CompletedTurnCount != 1 {
		t.Fatalf("RecordThreadUsageSnapshot() total=%#v err=%v", total, err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordThreadUsageSnapshotIgnoresOutOfOrderCounters(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &storage.Store{DB: db}
	record := observability.ThreadUsageSnapshotRecord{TraceID: "late-trace", ThreadID: "thread-1", TurnID: "late-turn", Session: observability.SessionUsageKey{AppID: "app-1", ChatType: "p2p", ChatID: "oc-1"}, Usage: observability.Usage{InputTokens: 120, OutputTokens: 25, CachedInputTokens: 70, ReasoningOutputTokens: 8, TotalTokens: 145}}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT input_tokens,output_tokens,cached_input_tokens,reasoning_output_tokens,total_tokens FROM thread_usage_snapshots").WithArgs("app-1", "p2p", "oc-1", "thread-1").WillReturnRows(sqlmock.NewRows([]string{"input_tokens", "output_tokens", "cached_input_tokens", "reasoning_output_tokens", "total_tokens"}).AddRow(130, 30, 75, 9, 160))
	// No UPDATE on thread_usage_snapshots: all old trace/turn and high-water
	// counters remain attached to the persisted thread row.
	mock.ExpectExec("INSERT IGNORE INTO session_usage_totals").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT completed_turn_count FROM session_usage_totals").WillReturnRows(sqlmock.NewRows([]string{"completed_turn_count"}).AddRow(1))
	mock.ExpectQuery("SELECT COALESCE\\(SUM\\(input_tokens\\),0\\)").WillReturnRows(sqlmock.NewRows([]string{"input_tokens", "output_tokens", "cached_input_tokens", "reasoning_output_tokens", "total_tokens"}).AddRow(130, 30, 75, 9, 160))
	mock.ExpectExec("UPDATE session_usage_totals").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	total, err := store.RecordThreadUsageSnapshot(context.Background(), record)
	if err != nil || total.TotalTokens != 160 || total.CompletedTurnCount != 1 {
		t.Fatalf("RecordThreadUsageSnapshot() total=%#v err=%v", total, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
