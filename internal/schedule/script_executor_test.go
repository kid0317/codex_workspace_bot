package schedule

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestScriptOutputCaptureCapsCombinedStreamsAndRetainsTheirSeparateBytes(t *testing.T) {
	capture := newScriptOutputCapture(5)
	if _, err := capture.stdoutWriter().Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := capture.stderrWriter().Write([]byte("defg")); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, combined, truncated := capture.Snapshot()
	if string(stdout) != "abc" || string(stderr) != "de" || string(combined) != "abc\nde" || !truncated {
		t.Fatalf("stdout=%q stderr=%q combined=%q truncated=%t", stdout, stderr, combined, truncated)
	}
}

func TestScriptExecutorMetadataKeepsOnlyDomainSeparatedDigests(t *testing.T) {
	protector := testProtector(t)
	executor := ScriptExecutor{Repository: &Repository{Protector: protector}}
	metadata, err := executor.Metadata(ScriptExecutionResult{ExitCode: 7, Stdout: []byte("out"), Stderr: []byte("err"), Truncated: true})
	if err != nil {
		t.Fatalf("Metadata() error=%v", err)
	}
	if metadata.ExitCode != 7 || metadata.StdoutHMAC == "" || metadata.StderrHMAC == "" || metadata.StdoutHMAC == metadata.StderrHMAC || metadata.OutputBytes != 6 || !metadata.Truncated {
		t.Fatalf("metadata=%#v", metadata)
	}
}

func TestScriptExecutorQueuesClaimedRunBeforeStartingIt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.July, 14, 2, 29, 0, 0, time.UTC)
	mock.ExpectExec("UPDATE scheduled_task_runs SET state='queued' WHERE id=\\? AND claim_token=\\? AND state='claimed'").
		WithArgs("run-1", "claim-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE scheduled_task_runs SET state='running',started_at=COALESCE\\(started_at,\\?\\),lease_until=\\? WHERE id=\\? AND claim_token=\\? AND state='queued'").
		WithArgs(now, now.Add(330*time.Second), "run-1", "claim-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	executor := ScriptExecutor{Repository: &Repository{DB: db, Now: func() time.Time { return now }}, Config: ScriptExecutionConfig{Timeout: 300 * time.Second}}
	if err := executor.MarkRunRunning(context.Background(), "run-1", "claim-1"); err != nil {
		t.Fatalf("MarkRunRunning() error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestScriptExecutorRunsDirectCommandInTaskWorkspace(t *testing.T) {
	workspace := t.TempDir()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("SELECT a.workspace_dir,a.id,cg.chat_type,cg.chat_id,t.creator_open_id FROM scheduled_task_runs").
		WithArgs("run-1", "claim-1").
		WillReturnRows(sqlmock.NewRows([]string{"workspace_dir", "app_id", "chat_type", "chat_id", "creator_open_id"}).AddRow(workspace, "app-1", "p2p", "oc-1", "ou-1"))
	executor := ScriptExecutor{Repository: &Repository{DB: db}, Config: ScriptExecutionConfig{ShellPath: "/bin/sh", Timeout: 5 * time.Second}}
	result, err := executor.Execute(context.Background(), ClaimedRun{ID: "run-1", ClaimToken: "claim-1", Kind: TaskScript, Payload: []byte("printf direct")})
	if err != nil {
		t.Fatalf("Execute() error=%v", err)
	}
	if result.ExitCode != 0 || !bytes.Equal(result.Stdout, []byte("direct")) || len(result.Stderr) != 0 {
		t.Fatalf("result=%#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestScriptExecutorReturnsStableTimeoutExitCode(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("SELECT a.workspace_dir,a.id,cg.chat_type,cg.chat_id,t.creator_open_id FROM scheduled_task_runs").
		WithArgs("run-timeout", "claim-timeout").
		WillReturnRows(sqlmock.NewRows([]string{"workspace_dir", "app_id", "chat_type", "chat_id", "creator_open_id"}).AddRow(t.TempDir(), "app-1", "p2p", "oc-1", "ou-1"))
	executor := ScriptExecutor{Repository: &Repository{DB: db}, Config: ScriptExecutionConfig{ShellPath: "/bin/sh", Timeout: 50 * time.Millisecond, MaxOutputBytes: 1024}}
	result, err := executor.Execute(context.Background(), ClaimedRun{ID: "run-timeout", ClaimToken: "claim-timeout", Kind: TaskScript, Payload: []byte("sleep 2")})
	if err != nil {
		t.Fatalf("Execute() error=%v", err)
	}
	if result.ExitCode != 124 {
		t.Fatalf("timeout exit code=%d stderr=%q want 124", result.ExitCode, result.Stderr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
