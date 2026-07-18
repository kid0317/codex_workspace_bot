package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRepositorySkipMisfireWritesSkippedRunAndAdvancesTask(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.July, 13, 4, 10, 0, 0, time.UTC)
	slot := time.Date(2026, time.July, 13, 4, 0, 0, 0, time.UTC)
	due := DueTask{ID: "task-1", Version: 2, NextRunAt: slot, Kind: TaskPrompt}
	repo := Repository{DB: db, Now: func() time.Time { return now }, NewID: sequentialIDs("run-1")}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT t.kind,t.cron_expression,t.silent,t.enabled,t.version,t.next_run_at,t.payload_text").WithArgs("task-1").
		WillReturnRows(sqlmock.NewRows([]string{"kind", "cron_expression", "silent", "enabled", "version", "next_run_at", "payload_text", "app_enabled", "schedule_enabled"}).AddRow("prompt", "*/5 * * * *", false, true, uint64(2), slot, "prompt", true, true))
	mock.ExpectExec("INSERT INTO scheduled_task_runs").WithArgs("run-1", "task-1", slot, uint64(2), "prompt", false, "prompt", "skipped", "skipped_misfire", now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE scheduled_tasks SET next_run_at=\\?,last_run_at=\\? WHERE id=\\? AND version=\\? AND next_run_at=\\?").WithArgs(time.Date(2026, time.July, 13, 4, 15, 0, 0, time.UTC), now, "task-1", uint64(2), slot).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := repo.SkipMisfire(context.Background(), due, now); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryFailRouteRevokedWritesFailedRunAndAdvancesTask(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.July, 13, 4, 10, 0, 0, time.UTC)
	slot := time.Date(2026, time.July, 13, 4, 0, 0, 0, time.UTC)
	due := DueTask{ID: "task-1", Version: 2, NextRunAt: slot, Kind: TaskPrompt, RouteRevoked: true}
	repo := Repository{DB: db, Now: func() time.Time { return now }, NewID: sequentialIDs("run-route-revoked")}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT t.kind,t.cron_expression,t.silent,t.enabled,t.version,t.next_run_at,t.payload_text").WithArgs("task-1").
		WillReturnRows(sqlmock.NewRows([]string{"kind", "cron_expression", "silent", "enabled", "version", "next_run_at", "payload_text", "app_enabled", "schedule_enabled"}).AddRow("prompt", "*/5 * * * *", false, true, uint64(2), slot, "prompt", false, true))
	mock.ExpectExec("INSERT INTO scheduled_task_runs").WithArgs("run-route-revoked", "task-1", slot, uint64(2), "prompt", false, "prompt", "failed", "failed_route_revoked", now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE scheduled_tasks SET next_run_at=\\?,last_run_at=\\? WHERE id=\\? AND version=\\? AND next_run_at=\\?").WithArgs(time.Date(2026, time.July, 13, 4, 15, 0, 0, time.UTC), now, "task-1", uint64(2), slot).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := repo.FailRouteRevoked(context.Background(), due, now); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
