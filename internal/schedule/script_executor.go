package schedule

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/observability"
)

type ScriptExecutionConfig struct {
	ShellPath      string
	Timeout        time.Duration
	MaxOutputBytes int64
}

type ScriptExecutionResult struct {
	ExitCode               int
	Stdout, Stderr, Output []byte
	Truncated              bool
}

type ScriptExecutor struct {
	Repository    *Repository
	Config        ScriptExecutionConfig
	Observability *observability.Recorder
}

// ScriptRunService is the scheduler-facing boundary for a claimed Script.
// Keeping its state transitions here lets DispatcherMux be tested without a
// database or a real command process, while production still uses ScriptExecutor.
type ScriptRunService interface {
	MarkRunRunning(context.Context, string, string) error
	Execute(context.Context, ClaimedRun) (ScriptExecutionResult, error)
	Metadata(ScriptExecutionResult) (ScriptRunMetadata, error)
	CompleteScriptRun(context.Context, RunCompletion, ScriptRunMetadata) error
	FailBeforeExecution(context.Context, string, string, string) error
}

func (e ScriptExecutor) MarkRunRunning(ctx context.Context, runID, claimToken string) error {
	if e.Repository == nil {
		return fmt.Errorf("scheduled script repository is unavailable")
	}
	if err := e.Repository.MarkRunQueued(ctx, runID, claimToken); err != nil {
		return fmt.Errorf("queue scheduled script run: %w", err)
	}
	lease := e.Config.Timeout + 30*time.Second
	if lease <= 30*time.Second {
		lease = 330 * time.Second
	}
	return e.Repository.MarkRunRunning(ctx, runID, claimToken, lease)
}

func (e ScriptExecutor) CompleteScriptRun(ctx context.Context, completion RunCompletion, metadata ScriptRunMetadata) error {
	if e.Repository == nil {
		return fmt.Errorf("scheduled script repository is unavailable")
	}
	return e.Repository.CompleteScriptRun(ctx, completion, metadata)
}

func (e ScriptExecutor) FailBeforeExecution(ctx context.Context, runID, claimToken, code string) error {
	if e.Repository == nil {
		return fmt.Errorf("scheduled script repository is unavailable")
	}
	return e.Repository.CompleteRun(ctx, RunCompletion{RunID: runID, ClaimToken: claimToken, State: RunFailed, ErrorCode: code})
}

// Metadata derives the only persistent representation of captured console.
// Prefixes ensure stdout and stderr cannot be substituted for each other even
// when their raw bytes happen to match.
func (e ScriptExecutor) Metadata(result ScriptExecutionResult) (ScriptRunMetadata, error) {
	if e.Repository == nil {
		return ScriptRunMetadata{}, fmt.Errorf("scheduled script repository is unavailable")
	}
	stdoutHMAC, _, err := e.Repository.Protector.Payloads.HMAC(append([]byte("scheduled_script_stdout\x00"), result.Stdout...))
	if err != nil {
		return ScriptRunMetadata{}, fmt.Errorf("digest scheduled script stdout: %w", err)
	}
	stderrHMAC, _, err := e.Repository.Protector.Payloads.HMAC(append([]byte("scheduled_script_stderr\x00"), result.Stderr...))
	if err != nil {
		return ScriptRunMetadata{}, fmt.Errorf("digest scheduled script stderr: %w", err)
	}
	return ScriptRunMetadata{ExitCode: result.ExitCode, StdoutHMAC: stdoutHMAC, StderrHMAC: stderrHMAC, OutputBytes: int64(len(result.Stdout) + len(result.Stderr)), Truncated: result.Truncated}, nil
}

func (e ScriptExecutor) Execute(ctx context.Context, run ClaimedRun) (result ScriptExecutionResult, err error) {
	if e.Repository == nil || run.Kind != TaskScript || len(run.Payload) == 0 {
		return ScriptExecutionResult{}, fmt.Errorf("scheduled script executor identity is invalid")
	}
	if e.Config.ShellPath == "" {
		return ScriptExecutionResult{}, fmt.Errorf("scheduled script executor is not configured")
	}
	var workspace, appID, chatType, chatID, ownerOpenID string
	err = e.Repository.DB.QueryRowContext(ctx, `SELECT a.workspace_dir,a.id,cg.chat_type,cg.chat_id,t.creator_open_id FROM scheduled_task_runs sr JOIN scheduled_tasks t ON t.id=sr.task_id JOIN apps a ON a.id=t.app_id JOIN chat_groups cg ON cg.id=t.chat_group_id WHERE sr.id=? AND sr.claim_token=? AND sr.state='running' AND sr.kind='script'`, run.ID, run.ClaimToken).Scan(&workspace, &appID, &chatType, &chatID, &ownerOpenID)
	if err == sql.ErrNoRows {
		return ScriptExecutionResult{}, fmt.Errorf("claimed scheduled script is no longer runnable")
	}
	if err != nil {
		return ScriptExecutionResult{}, fmt.Errorf("load claimed script workspace: %w", err)
	}
	var attempt *observability.Attempt
	if e.Observability != nil && run.TraceID != "" {
		attempt, _ = e.Observability.Start(observability.AttemptMetadata{TraceID: run.TraceID, Name: "scheduled.script", SessionID: appID + ":" + chatType + ":" + chatID, UserID: ownerOpenID, Input: map[string]any{"command": string(run.Payload), "run_id": run.ID, "task_id": run.TaskID}, Metadata: map[string]string{"app_id": appID, "chat_type": chatType, "chat_id": chatID, "user_open_id": ownerOpenID, "task_type": "scheduled", "scheduled_task_id": run.TaskID, "scheduled_run_id": run.ID}})
	}
	var collector *scriptOutputCapture
	defer func() {
		if attempt != nil {
			if collector != nil {
				stdout, stderr := collector.FullSnapshot()
				attempt.RecordBusinessPayload("scheduled.script.stdout", map[string]any{"content_encoding": "base64", "content_base64": base64.StdEncoding.EncodeToString(stdout)})
				attempt.RecordBusinessPayload("scheduled.script.stderr", map[string]any{"content_encoding": "base64", "content_base64": base64.StdEncoding.EncodeToString(stderr)})
			}
			attempt.CloseProtocol(nil, err)
			attempt.End(map[string]any{"exit_code": result.ExitCode, "stdout": result.Stdout, "stderr": result.Stderr, "output": result.Output, "truncated": result.Truncated}, err)
		}
	}()
	max := e.Config.MaxOutputBytes
	if max <= 0 {
		max = 24 * 1024
	}
	timeout := e.Config.Timeout
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, e.Config.ShellPath, "-c", string(run.Payload))
	cmd.Dir = workspace
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	collector = newScriptOutputCapture(max)
	cmd.Stdout = collector.stdoutWriter()
	cmd.Stderr = collector.stderrWriter()
	if err := cmd.Start(); err != nil {
		return ScriptExecutionResult{}, fmt.Errorf("start scheduled script command: %w", err)
	}
	err = cmd.Wait()
	stdoutBytes, stderrBytes, output, truncated := collector.Snapshot()
	result = ScriptExecutionResult{ExitCode: 0, Stdout: stdoutBytes, Stderr: stderrBytes, Output: output, Truncated: truncated}
	if exit, ok := err.(*exec.ExitError); ok {
		if runCtx.Err() == context.DeadlineExceeded {
			result.ExitCode = 124
			return result, nil
		}
		result.ExitCode = exit.ExitCode()
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("wait scheduled script command: %w", err)
	}
	return result, nil
}

type scriptOutputCapture struct {
	mu                     sync.Mutex
	limit, used            int64
	stdout, stderr         bytes.Buffer
	fullStdout, fullStderr bytes.Buffer
	truncated              bool
}

type scriptOutputWriter struct {
	capture *scriptOutputCapture
	stderr  bool
}

func newScriptOutputCapture(limit int64) *scriptOutputCapture {
	if limit < 1 {
		limit = 1
	}
	return &scriptOutputCapture{limit: limit}
}
func (c *scriptOutputCapture) stdoutWriter() io.Writer { return scriptOutputWriter{capture: c} }
func (c *scriptOutputCapture) stderrWriter() io.Writer {
	return scriptOutputWriter{capture: c, stderr: true}
}
func (w scriptOutputWriter) Write(data []byte) (int, error) {
	w.capture.mu.Lock()
	defer w.capture.mu.Unlock()
	if w.stderr {
		_, _ = w.capture.fullStderr.Write(data)
	} else {
		_, _ = w.capture.fullStdout.Write(data)
	}
	remain := w.capture.limit - w.capture.used
	if remain <= 0 {
		w.capture.truncated = true
		return len(data), nil
	}
	accepted := data
	if int64(len(accepted)) > remain {
		accepted = accepted[:remain]
		w.capture.truncated = true
	}
	if w.stderr {
		_, _ = w.capture.stderr.Write(accepted)
	} else {
		_, _ = w.capture.stdout.Write(accepted)
	}
	w.capture.used += int64(len(accepted))
	return len(data), nil
}
func (c *scriptOutputCapture) FullSnapshot() (stdout, stderr []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.fullStdout.Bytes()...), append([]byte(nil), c.fullStderr.Bytes()...)
}
func (c *scriptOutputCapture) Snapshot() (stdout, stderr, combined []byte, truncated bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stdout = append([]byte(nil), c.stdout.Bytes()...)
	stderr = append([]byte(nil), c.stderr.Bytes()...)
	combined = append([]byte(nil), stdout...)
	if len(stdout) > 0 && len(stderr) > 0 {
		combined = append(combined, '\n')
	}
	combined = append(combined, stderr...)
	return stdout, stderr, combined, c.truncated
}
