package schedule

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRepositoryCompleteRunRequiresMatchingClaimToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.July, 13, 2, 3, 4, 0, time.UTC)
	mock.ExpectExec("UPDATE scheduled_task_runs SET state=\\?,error_code=\\?,lease_until=NULL,completed_at=\\?,duration_ms=\\?,thread_id=COALESCE\\(\\?,thread_id\\),turn_id=COALESCE\\(\\?,turn_id\\) WHERE id=\\? AND claim_token=\\? AND state IN \\('claimed','queued','running'\\)").
		WithArgs("succeeded", nil, now, int64(250), "thread-1", "turn-1", "run-1", "token-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := (&Repository{DB: db, Now: func() time.Time { return now }}).CompleteRun(context.Background(), RunCompletion{RunID: "run-1", ClaimToken: "token-1", State: RunSucceeded, StartedAt: now.Add(-250 * time.Millisecond), ThreadID: "thread-1", TurnID: "turn-1"}); err != nil {
		t.Fatalf("CompleteRun() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryCompleteRunRejectsMismatchedClaimToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec("UPDATE scheduled_task_runs SET state=\\?").WillReturnResult(sqlmock.NewResult(0, 0))
	err = (&Repository{DB: db}).CompleteRun(context.Background(), RunCompletion{RunID: "run-1", ClaimToken: "wrong", State: RunFailed})
	if !errors.Is(err, ErrRunClaimLost) {
		t.Fatalf("error=%v want ErrRunClaimLost", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryCompleteScriptRunPersistsOnlyOutputMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.July, 13, 3, 4, 5, 0, time.UTC)
	mock.ExpectExec("UPDATE scheduled_task_runs SET state=\\?,error_code=\\?,lease_until=NULL,completed_at=\\?,duration_ms=\\?,exit_code=\\?,stdout_hmac=\\?,stderr_hmac=\\?,output_bytes=\\?,output_truncated=\\? WHERE id=\\?").
		WithArgs("failed", "script_exit", now, int64(42), 7, "stdout-hmac", "stderr-hmac", int64(13), true, "run-1", "token-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	err = (&Repository{DB: db, Now: func() time.Time { return now }}).CompleteScriptRun(context.Background(), RunCompletion{RunID: "run-1", ClaimToken: "token-1", State: RunFailed, ErrorCode: "script_exit", DurationMS: 42}, ScriptRunMetadata{ExitCode: 7, StdoutHMAC: "stdout-hmac", StderrHMAC: "stderr-hmac", OutputBytes: 13, Truncated: true})
	if err != nil {
		t.Fatalf("CompleteScriptRun() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
