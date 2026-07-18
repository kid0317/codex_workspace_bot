package schedule

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRepositoryCreateTaskStoresOwnerAndPayloadAsPlaintext(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	protector := testProtector(t)
	now := time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC)
	repo := Repository{DB: db, Protector: protector, Now: func() time.Time { return now }}

	mock.ExpectExec("INSERT INTO scheduled_tasks").
		WithArgs(
			"task-1", "app-1", "group-1", sqlmock.AnyArg(), "user-1",
			"prompt", "*/5 * * * *", "Asia/Shanghai", "private prompt", false,
			time.Date(2026, time.July, 13, 1, 5, 0, 0, time.UTC),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	task, err := repo.CreateTask(context.Background(), TaskDraft{
		ID:             "task-1",
		Owner:          Owner{AppID: "app-1", ChatGroupID: "group-1", OpenID: "user-1"},
		Kind:           TaskPrompt,
		CronExpression: "*/5 * * * *",
		Payload:        []byte("private prompt"),
		Silent:         false,
	})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if task.ID != "task-1" || task.Version != 1 || !task.NextRunAt.Equal(time.Date(2026, time.July, 13, 1, 5, 0, 0, time.UTC)) {
		t.Fatalf("CreateTask() = %#v", task)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryCreateTaskRejectsInvalidTaskBeforeSQL(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := Repository{DB: db, Protector: testProtector(t)}
	if _, err := repo.CreateTask(context.Background(), TaskDraft{ID: "task-1", Owner: Owner{AppID: "app-1", ChatGroupID: "group-1", OpenID: "user-1"}, Kind: TaskPrompt, CronExpression: "@hourly", Payload: []byte("private")}); err == nil {
		t.Fatal("CreateTask() accepted invalid Cron")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryUpdateTaskUsesOwnerAndVersionCAS(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	protector := testProtector(t)
	now := time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC)
	owner := Owner{AppID: "app-1", ChatGroupID: "group-1", OpenID: "user-1"}
	ownerHMAC, err := protector.OwnerHMAC(owner)
	if err != nil {
		t.Fatal(err)
	}
	repo := Repository{DB: db, Protector: protector, Now: func() time.Time { return now }}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT kind,cron_expression,silent,enabled,version,next_run_at,payload_text FROM scheduled_tasks").
		WithArgs("task-1", "app-1", "group-1", ownerHMAC).
		WillReturnRows(sqlmock.NewRows([]string{"kind", "cron_expression", "silent", "enabled", "version", "next_run_at", "payload_text"}).
			AddRow("prompt", "*/5 * * * *", false, true, uint64(1), time.Date(2026, time.July, 13, 1, 5, 0, 0, time.UTC), "private prompt"))
	mock.ExpectExec("UPDATE scheduled_tasks SET cron_expression").
		WithArgs("*/10 * * * *", "private prompt", false, true, uint64(2), time.Date(2026, time.July, 13, 1, 10, 0, 0, time.UTC), "task-1", "app-1", "group-1", ownerHMAC, uint64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	cron := "*/10 * * * *"
	task, err := repo.UpdateTask(context.Background(), TaskPatch{TaskID: "task-1", Owner: owner, ExpectedVersion: 1, CronExpression: &cron})
	if err != nil {
		t.Fatalf("UpdateTask() error = %v", err)
	}
	if task.Version != 2 || !task.NextRunAt.Equal(time.Date(2026, time.July, 13, 1, 10, 0, 0, time.UTC)) {
		t.Fatalf("UpdateTask() = %#v", task)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryUpdateTaskRejectsVersionConflict(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	protector := testProtector(t)
	owner := Owner{AppID: "app-1", ChatGroupID: "group-1", OpenID: "user-1"}
	ownerHMAC, err := protector.OwnerHMAC(owner)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT kind,cron_expression,silent,enabled,version,next_run_at,payload_text FROM scheduled_tasks").
		WithArgs("task-1", "app-1", "group-1", ownerHMAC).
		WillReturnRows(sqlmock.NewRows([]string{"kind", "cron_expression", "silent", "enabled", "version", "next_run_at", "payload_text"}).
			AddRow("prompt", "*/5 * * * *", false, true, uint64(2), time.Date(2026, time.July, 13, 1, 5, 0, 0, time.UTC), "private prompt"))
	mock.ExpectRollback()

	silent := true
	_, err = (&Repository{DB: db, Protector: protector}).UpdateTask(context.Background(), TaskPatch{TaskID: "task-1", Owner: owner, ExpectedVersion: 1, Silent: &silent})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("UpdateTask() error = %v, want ErrVersionConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryListOwnedTasksDecryptsOnlyMatchingOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	protector := testProtector(t)
	owner := Owner{AppID: "app-1", ChatGroupID: "group-1", OpenID: "user-1"}
	ownerHMAC, err := protector.OwnerHMAC(owner)
	if err != nil {
		t.Fatal(err)
	}
	updated := time.Date(2026, time.July, 13, 1, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT id,kind,cron_expression,timezone,silent,enabled,version,next_run_at,payload_text,updated_at FROM scheduled_tasks").
		WithArgs("app-1", "group-1", ownerHMAC, 2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "kind", "cron_expression", "timezone", "silent", "enabled", "version", "next_run_at", "payload_text", "updated_at"}).
			AddRow("task-1", "prompt", "*/5 * * * *", "Asia/Shanghai", false, true, uint64(2), time.Date(2026, time.July, 13, 1, 5, 0, 0, time.UTC), "private prompt", updated))

	page, err := (&Repository{DB: db, Protector: protector}).ListOwnedTasks(context.Background(), owner, nil, 1)
	if err != nil {
		t.Fatalf("ListOwnedTasks() error = %v", err)
	}
	if len(page.Tasks) != 1 || page.Tasks[0].Payload != "private prompt" || page.Next != nil {
		t.Fatalf("ListOwnedTasks() = %#v", page)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryClaimDueCreatesOneImmutablePromptRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC)
	slot := time.Date(2026, time.July, 13, 1, 0, 0, 0, time.UTC)
	payload := "prompt snapshot"
	repo := Repository{DB: db, Now: func() time.Time { return now }, NewID: sequentialIDs("run-1", "claim-1"), NewTraceID: func() (string, error) { return "0123456789abcdef0123456789abcdef", nil }}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT t.app_id,t.chat_group_id,t.creator_open_id_hmac,t.kind,t.cron_expression,t.silent,t.enabled,t.version,t.next_run_at,t.payload_text,a.enabled,cg.schedule_enabled FROM scheduled_tasks t").
		WithArgs("task-1").
		WillReturnRows(sqlmock.NewRows([]string{"app_id", "chat_group_id", "creator_open_id_hmac", "kind", "cron_expression", "silent", "enabled", "version", "next_run_at", "payload_text", "app_enabled", "schedule_enabled"}).
			AddRow("app-1", "group-1", "owner-hmac", "prompt", "*/5 * * * *", false, true, uint64(3), slot, payload, true, true))
	mock.ExpectExec("INSERT INTO scheduled_task_runs").
		WithArgs("run-1", "0123456789abcdef0123456789abcdef", "task-1", slot, uint64(3), "prompt", false, payload, nil, nil, nil, "claimed", "claim-1", now.Add(30*time.Second)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE scheduled_tasks SET next_run_at=\\?,last_run_at=\\? WHERE id=\\? AND version=\\? AND next_run_at=\\?").
		WithArgs(time.Date(2026, time.July, 13, 1, 5, 0, 0, time.UTC), now, "task-1", uint64(3), slot).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	run, err := repo.ClaimDue(context.Background(), DueClaim{TaskID: "task-1", ObservedVersion: 3, ObservedSlot: slot, Lease: 30 * time.Second})
	if err != nil {
		t.Fatalf("ClaimDue() error = %v", err)
	}
	if run.ID != "run-1" || run.TraceID != "0123456789abcdef0123456789abcdef" || run.ClaimToken != "claim-1" || run.TaskID != "task-1" || !bytes.Equal(run.Payload, []byte(payload)) {
		t.Fatalf("ClaimDue() = %#v", run)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryClaimDueSnapshotsDirectScriptCommand(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	protector := testProtector(t)
	now := time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC)
	slot := time.Date(2026, time.July, 13, 1, 0, 0, 0, time.UTC)
	owner := Owner{AppID: "app-1", ChatGroupID: "group-1", OpenID: "user-1"}
	ownerHMAC, err := protector.OwnerHMAC(owner)
	if err != nil {
		t.Fatal(err)
	}
	const command = "python /root/aipm-codex/check_today_holiday.py"
	repo := Repository{DB: db, Protector: protector, Now: func() time.Time { return now }, NewID: sequentialIDs("run-1", "claim-1"), NewTraceID: func() (string, error) { return "fedcba9876543210fedcba9876543210", nil }}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT t.app_id,t.chat_group_id,t.creator_open_id_hmac,t.kind").WithArgs("task-1").
		WillReturnRows(sqlmock.NewRows([]string{"app_id", "chat_group_id", "creator_open_id_hmac", "kind", "cron_expression", "silent", "enabled", "version", "next_run_at", "payload_text", "app_enabled", "schedule_enabled"}).
			AddRow("app-1", "group-1", ownerHMAC, "script", "*/5 * * * *", true, true, uint64(3), slot, command, true, true))
	mock.ExpectExec("INSERT INTO scheduled_task_runs").
		WithArgs("run-1", "fedcba9876543210fedcba9876543210", "task-1", slot, uint64(3), "script", true, command, nil, nil, nil, "claimed", "claim-1", now.Add(30*time.Second)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE scheduled_tasks SET next_run_at=\\?,last_run_at=\\? WHERE id=\\? AND version=\\? AND next_run_at=\\?").
		WithArgs(time.Date(2026, time.July, 13, 1, 5, 0, 0, time.UTC), now, "task-1", uint64(3), slot).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	run, err := repo.ClaimDue(context.Background(), DueClaim{TaskID: "task-1", ObservedVersion: 3, ObservedSlot: slot, Lease: 30 * time.Second})
	if err != nil || string(run.Payload) != command {
		t.Fatalf("run=%#v err=%v", run, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryClaimDueRejectsRouteOrVersionChangesWithoutSideEffect(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	slot := time.Date(2026, time.July, 13, 1, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT t.app_id,t.chat_group_id,t.creator_open_id_hmac,t.kind,t.cron_expression,t.silent,t.enabled,t.version,t.next_run_at,t.payload_text,a.enabled,cg.schedule_enabled FROM scheduled_tasks t").
		WithArgs("task-1").
		WillReturnRows(sqlmock.NewRows([]string{"app_id", "chat_group_id", "creator_open_id_hmac", "kind", "cron_expression", "silent", "enabled", "version", "next_run_at", "payload_text", "app_enabled", "schedule_enabled"}).
			AddRow("app-1", "group-1", "owner-hmac", "prompt", "*/5 * * * *", false, true, uint64(4), slot, "prompt", true, false))
	mock.ExpectRollback()

	_, err = (&Repository{DB: db, NewID: sequentialIDs("must-not-be-used")}).ClaimDue(context.Background(), DueClaim{TaskID: "task-1", ObservedVersion: 3, ObservedSlot: slot, Lease: 30 * time.Second})
	if !errors.Is(err, ErrDueClaimConflict) {
		t.Fatalf("ClaimDue() error = %v, want ErrDueClaimConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func sequentialIDs(values ...string) func() string {
	return func() string {
		if len(values) == 0 {
			return ""
		}
		value := values[0]
		values = values[1:]
		return value
	}
}

func testProtector(t *testing.T) Protector {
	t.Helper()
	payloads, err := NewKeyring([]Key{{Version: 1, Material: bytes.Repeat([]byte{7}, 32)}})
	if err != nil {
		t.Fatal(err)
	}
	owners, err := NewKeyring([]Key{{Version: 1, Material: bytes.Repeat([]byte{8}, 32)}})
	if err != nil {
		t.Fatal(err)
	}
	return Protector{Payloads: payloads, Owners: owners}
}
