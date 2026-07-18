package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

func TestRepositoryCreateTaskForToolCallCommitsLedgerTaskAndEncryptedResultTogether(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	protector := testProtector(t)
	now := time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC)
	repo := Repository{DB: db, Protector: protector, Now: func() time.Time { return now }}
	owner := Owner{AppID: "app-1", ChatGroupID: "group-1", OpenID: "user-1"}

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO scheduled_task_tool_calls").
		WithArgs(sqlmock.AnyArg(), "app-1", "p2p:ou-1:app-1", "group-1", "thread-1", "turn-1", "call-1", "schedule.create", "arguments-hmac", now.Add(time.Minute)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO scheduled_tasks").
		WithArgs("task-1", "app-1", "group-1", sqlmock.AnyArg(), "user-1", "prompt", "*/5 * * * *", "Asia/Shanghai", "private prompt", false, time.Date(2026, time.July, 13, 1, 5, 0, 0, time.UTC)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE scheduled_task_tool_calls SET state='succeeded'").
		WithArgs(sqlmock.AnyArg(), now, "app-1", "thread-1", "turn-1", "call-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := repo.CreateTaskForToolCall(context.Background(), ToolCallInput{Identity: testToolCallIdentity(), ArgumentsHMAC: "arguments-hmac", Lease: time.Minute}, TaskDraft{ID: "task-1", Owner: owner, Kind: TaskPrompt, CronExpression: "*/5 * * * *", Payload: []byte("private prompt")}, func(task Task) ([]byte, error) {
		return json.Marshal(map[string]any{"id": task.ID, "version": task.Version})
	})
	if err != nil {
		t.Fatalf("CreateTaskForToolCall() error = %v", err)
	}
	if result.Replay || string(result.Payload) != `{"id":"task-1","version":1}` {
		t.Fatalf("CreateTaskForToolCall() = %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryCreateTaskForToolCallRejectsOwnerQuotaBeforeTaskInsert(t *testing.T) {
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
	repo := Repository{DB: db, Protector: protector, MaxEnabledTasksPerOwner: 1, MaxEnabledTasksPerApp: 10}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO scheduled_task_tool_calls").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM scheduled_tasks WHERE app_id=\\? AND chat_group_id=\\? AND creator_open_id_hmac=\\? AND enabled=TRUE FOR UPDATE").WithArgs("app-1", "group-1", ownerHMAC).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()
	_, err = repo.CreateTaskForToolCall(context.Background(), ToolCallInput{Identity: testToolCallIdentity(), ArgumentsHMAC: "quota-hmac"}, TaskDraft{ID: "task-1", Owner: owner, Kind: TaskPrompt, CronExpression: "*/5 * * * *", Payload: []byte("private prompt")}, func(Task) ([]byte, error) { return []byte(`{}`), nil })
	if !errors.Is(err, ErrTaskQuota) {
		t.Fatalf("CreateTaskForToolCall() error=%v, want ErrTaskQuota", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryListOwnedTasksForToolCallCommitsExactPageForReplay(t *testing.T) {
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
	updated := time.Date(2026, time.July, 13, 1, 0, 0, 0, time.UTC)
	repo := Repository{DB: db, Protector: protector, Now: func() time.Time { return now }}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO scheduled_task_tool_calls").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT id,kind,cron_expression,timezone,silent,enabled,version,next_run_at,payload_text,updated_at FROM scheduled_tasks").
		WithArgs("app-1", "group-1", ownerHMAC, 2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "kind", "cron_expression", "timezone", "silent", "enabled", "version", "next_run_at", "payload_text", "updated_at"}).AddRow("task-1", "prompt", "*/5 * * * *", "Asia/Shanghai", false, true, uint64(1), time.Date(2026, time.July, 13, 1, 5, 0, 0, time.UTC), "private prompt", updated))
	mock.ExpectExec("UPDATE scheduled_task_tool_calls SET state='succeeded'").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := repo.ListOwnedTasksForToolCall(context.Background(), ToolCallInput{Identity: ToolCallIdentity{AppID: "app-1", ChannelKey: "p2p:ou-1:app-1", ChatGroupID: "group-1", ThreadID: "thread-1", TurnID: "turn-1", CallID: "call-list", Tool: "schedule.list_own"}, ArgumentsHMAC: "list-hmac", Lease: time.Minute}, owner, nil, 1, func(page TaskPage) ([]byte, error) {
		return json.Marshal(map[string]any{"count": len(page.Tasks), "id": page.Tasks[0].ID})
	})
	if err != nil || result.Replay || !result.Success || string(result.Payload) != `{"count":1,"id":"task-1"}` {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryUpdateTaskForToolCallCommitsCASAndEncryptedResultTogether(t *testing.T) {
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
	mock.ExpectExec("INSERT INTO scheduled_task_tool_calls").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT kind,cron_expression,silent,enabled,version,next_run_at,payload_text FROM scheduled_tasks").
		WithArgs("task-1", "app-1", "group-1", ownerHMAC).
		WillReturnRows(sqlmock.NewRows([]string{"kind", "cron_expression", "silent", "enabled", "version", "next_run_at", "payload_text"}).AddRow("prompt", "*/5 * * * *", false, true, uint64(1), time.Date(2026, time.July, 13, 1, 5, 0, 0, time.UTC), "private prompt"))
	mock.ExpectExec("UPDATE scheduled_tasks SET cron_expression").
		WithArgs("*/10 * * * *", "private prompt", false, true, uint64(2), time.Date(2026, time.July, 13, 1, 10, 0, 0, time.UTC), "task-1", "app-1", "group-1", ownerHMAC, uint64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE scheduled_task_tool_calls SET state='succeeded'").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	cron := "*/10 * * * *"
	result, err := repo.UpdateTaskForToolCall(context.Background(), ToolCallInput{Identity: ToolCallIdentity{AppID: "app-1", ChannelKey: "p2p:ou-1:app-1", ChatGroupID: "group-1", ThreadID: "thread-1", TurnID: "turn-1", CallID: "call-update", Tool: "schedule.update"}, ArgumentsHMAC: "update-hmac", Lease: time.Minute}, TaskPatch{TaskID: "task-1", Owner: owner, ExpectedVersion: 1, CronExpression: &cron}, func(task Task) ([]byte, error) {
		return json.Marshal(map[string]any{"id": task.ID, "version": task.Version})
	})
	if err != nil || result.Replay || !result.Success || string(result.Payload) != `{"id":"task-1","version":2}` {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryClaimToolCallRejectsSameCallWithDifferentArguments(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec("INSERT INTO scheduled_task_tool_calls").WillReturnError(&mysql.MySQLError{Number: 1062, Message: "duplicate"})
	mock.ExpectQuery("SELECT arguments_hmac,state,result_text,error_code,lease_until FROM scheduled_task_tool_calls").
		WithArgs("app-1", "thread-1", "turn-1", "call-1").
		WillReturnRows(sqlmock.NewRows([]string{"arguments_hmac", "state", "result_text", "error_code", "lease_until"}).AddRow("different", "claimed", nil, nil, time.Now().Add(time.Minute)))
	claim, err := (&Repository{DB: db}).ClaimToolCall(context.Background(), ToolCallInput{Identity: testToolCallIdentity(), ArgumentsHMAC: "current", Lease: time.Minute})
	if err != nil || claim.Kind != ToolCallConflict {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryClaimToolCallReturnsCompletedReplay(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec("INSERT INTO scheduled_task_tool_calls").WillReturnError(&mysql.MySQLError{Number: 1062, Message: "duplicate"})
	mock.ExpectQuery("SELECT arguments_hmac,state,result_text,error_code,lease_until FROM scheduled_task_tool_calls").
		WithArgs("app-1", "thread-1", "turn-1", "call-1").
		WillReturnRows(sqlmock.NewRows([]string{"arguments_hmac", "state", "result_text", "error_code", "lease_until"}).AddRow("same", "succeeded", `{"id":"task-1"}`, nil, nil))
	claim, err := (&Repository{DB: db}).ClaimToolCall(context.Background(), ToolCallInput{Identity: testToolCallIdentity(), ArgumentsHMAC: "same", Lease: time.Minute})
	if err != nil || claim.Kind != ToolCallReplay || string(claim.Replay.Payload) != `{"id":"task-1"}` {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func testToolCallIdentity() ToolCallIdentity {
	return ToolCallIdentity{AppID: "app-1", ChannelKey: "p2p:ou-1:app-1", ChatGroupID: "group-1", ThreadID: "thread-1", TurnID: "turn-1", CallID: "call-1", Tool: "schedule.create"}
}

func TestRepositoryReconcileExpiredToolClaimsRejectsWithoutMutationReplay(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.July, 13, 3, 4, 5, 0, time.UTC)
	mock.ExpectExec("UPDATE scheduled_task_tool_calls SET state='rejected',error_code='tool_call_lease_expired',lease_until=NULL,completed_at=\\? WHERE state IN \\('claimed','in_flight'\\) AND lease_until < \\?").WithArgs(now, now).WillReturnResult(sqlmock.NewResult(0, 2))
	count, err := (&Repository{DB: db}).ReconcileExpiredToolCalls(context.Background(), now)
	if err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
