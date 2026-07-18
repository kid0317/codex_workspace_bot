package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

func TestPromptDispatcherQueuesRunBeforeImmediateWorkerStart(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.July, 14, 2, 50, 0, 0, time.UTC)
	repository := &Repository{DB: db, Now: func() time.Time { return now }}
	run := ClaimedRun{ID: "run-1", TaskID: "task-1", TaskVersion: 2, Kind: TaskPrompt, ClaimToken: "claim-1", Payload: []byte("please prepare the report")}
	mock.ExpectQuery("SELECT a.id,a.workspace_dir").
		WithArgs("run-1", "task-1", uint64(2), "claim-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "workspace_dir", "workspace_mode", "model", "reasoning_effort", "chat_group_id", "chat_type", "chat_id", "creator_open_id"}).
			AddRow("app-1", "/workspace", "work", "model", "medium", "group-1", "p2p", "ou-1", "ou-1"))
	mock.ExpectExec("UPDATE scheduled_task_runs SET state='queued' WHERE id=\\? AND claim_token=\\? AND state='claimed'").
		WithArgs("run-1", "claim-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE scheduled_task_runs SET state='running',started_at=COALESCE\\(started_at,\\?\\),lease_until=\\? WHERE id=\\? AND claim_token=\\? AND state='queued'").
		WithArgs(now, now.Add(time.Minute), "run-1", "claim-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	dispatcher := PromptDispatcher{Repository: repository, Workers: immediatePromptWorker{repository: repository, lease: time.Minute}, Now: func() time.Time { return now }}
	if err := dispatcher.Dispatch(context.Background(), run); err != nil {
		t.Fatalf("Dispatch() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type immediatePromptWorker struct {
	repository *Repository
	lease      time.Duration
}

func (w immediatePromptWorker) Accept(ctx context.Context, message worker.Message) error {
	return w.repository.MarkRunRunning(ctx, message.ScheduledTaskRunID, message.ScheduledClaimToken, w.lease)
}
