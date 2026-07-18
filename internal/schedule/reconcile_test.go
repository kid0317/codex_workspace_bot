package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRepositoryReconcileInterruptedRunsNeverReplaysClaimedQueuedOrRunning(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC)
	mock.ExpectExec("UPDATE scheduled_task_runs SET state='unknown',error_code='unknown_interrupted',lease_until=NULL,completed_at=\\? WHERE state IN \\('claimed','queued','running'\\)").
		WithArgs(now).
		WillReturnResult(sqlmock.NewResult(0, 2))

	count, err := (&Repository{DB: db}).ReconcileInterruptedRuns(context.Background(), now)
	if err != nil {
		t.Fatalf("ReconcileInterruptedRuns() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("ReconcileInterruptedRuns() = %d, want 2", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
