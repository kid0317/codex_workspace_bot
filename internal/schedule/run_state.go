package schedule

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RunState string

const (
	RunSucceeded RunState = "succeeded"
	RunFailed    RunState = "failed"
	RunCancelled RunState = "cancelled"
	RunUnknown   RunState = "unknown"
	RunSkipped   RunState = "skipped"
)

var ErrRunClaimLost = errors.New("schedule run claim is no longer active")

func (r Repository) MarkRunQueued(ctx context.Context, runID, claimToken string) error {
	if r.DB == nil {
		return fmt.Errorf("schedule store database is required")
	}
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(claimToken) == "" {
		return fmt.Errorf("schedule queued run identity is invalid")
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE scheduled_task_runs SET state='queued' WHERE id=? AND claim_token=? AND state='claimed'`, runID, claimToken)
	if err != nil {
		return fmt.Errorf("mark scheduled run queued: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read scheduled queued result: %w", err)
	}
	if count != 1 {
		return ErrRunClaimLost
	}
	return nil
}

// RunCompletion is intentionally terminal-only. Scheduler, Worker and the
// script executor must supply the exact claim token obtained from ClaimDue;
// a stale worker cannot overwrite a later owner's terminal decision.
type RunCompletion struct {
	RunID, ClaimToken string
	State             RunState
	ErrorCode         string
	StartedAt         time.Time
	DurationMS        int64
	// ThreadID and TurnID are populated only for scheduled Prompt runs. They
	// are persisted with the same token-conditional terminal transition, so a
	// stale Worker cannot attach an App Server attempt to a later run owner.
	ThreadID, TurnID string
}

// ScriptRunMetadata contains only keyed output digests and bounded capture
// metadata. Console bytes never enter MySQL or logs.
type ScriptRunMetadata struct {
	ExitCode               int
	StdoutHMAC, StderrHMAC string
	OutputBytes            int64
	Truncated              bool
}

func (r Repository) CompleteRun(ctx context.Context, completion RunCompletion) error {
	if r.DB == nil {
		return fmt.Errorf("schedule store database is required")
	}
	if strings.TrimSpace(completion.RunID) == "" || strings.TrimSpace(completion.ClaimToken) == "" || !completion.State.terminal() {
		return fmt.Errorf("schedule run completion is invalid")
	}
	if completion.State == RunSucceeded && completion.ErrorCode != "" {
		return fmt.Errorf("successful schedule run cannot have an error code")
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	duration := completion.DurationMS
	if duration < 0 {
		return fmt.Errorf("schedule run duration is invalid")
	}
	if duration == 0 && !completion.StartedAt.IsZero() && !now.Before(completion.StartedAt) {
		duration = now.Sub(completion.StartedAt).Milliseconds()
	}
	var errorCode any
	if completion.ErrorCode != "" {
		errorCode = completion.ErrorCode
	}
	var threadID, turnID any
	if completion.ThreadID != "" {
		threadID = completion.ThreadID
	}
	if completion.TurnID != "" {
		turnID = completion.TurnID
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE scheduled_task_runs SET state=?,error_code=?,lease_until=NULL,completed_at=?,duration_ms=?,thread_id=COALESCE(?,thread_id),turn_id=COALESCE(?,turn_id) WHERE id=? AND claim_token=? AND state IN ('claimed','queued','running')`, string(completion.State), errorCode, now, duration, threadID, turnID, completion.RunID, completion.ClaimToken)
	if err != nil {
		return fmt.Errorf("complete scheduled task run: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read schedule run completion result: %w", err)
	}
	if changed != 1 {
		return ErrRunClaimLost
	}
	return nil
}

func (r Repository) CompleteScriptRun(ctx context.Context, completion RunCompletion, metadata ScriptRunMetadata) error {
	if r.DB == nil {
		return fmt.Errorf("schedule store database is required")
	}
	if strings.TrimSpace(completion.RunID) == "" || strings.TrimSpace(completion.ClaimToken) == "" || !completion.State.terminal() || metadata.OutputBytes < 0 || strings.TrimSpace(metadata.StdoutHMAC) == "" || strings.TrimSpace(metadata.StderrHMAC) == "" {
		return fmt.Errorf("schedule script run completion is invalid")
	}
	if completion.State == RunSucceeded && completion.ErrorCode != "" {
		return fmt.Errorf("successful schedule run cannot have an error code")
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	duration := completion.DurationMS
	if duration < 0 {
		return fmt.Errorf("schedule run duration is invalid")
	}
	if duration == 0 && !completion.StartedAt.IsZero() && !now.Before(completion.StartedAt) {
		duration = now.Sub(completion.StartedAt).Milliseconds()
	}
	var errorCode any
	if completion.ErrorCode != "" {
		errorCode = completion.ErrorCode
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE scheduled_task_runs SET state=?,error_code=?,lease_until=NULL,completed_at=?,duration_ms=?,exit_code=?,stdout_hmac=?,stderr_hmac=?,output_bytes=?,output_truncated=? WHERE id=? AND claim_token=? AND state IN ('claimed','queued','running')`, string(completion.State), errorCode, now, duration, metadata.ExitCode, metadata.StdoutHMAC, metadata.StderrHMAC, metadata.OutputBytes, metadata.Truncated, completion.RunID, completion.ClaimToken)
	if err != nil {
		return fmt.Errorf("complete scheduled script run: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read scheduled script completion result: %w", err)
	}
	if changed != 1 {
		return ErrRunClaimLost
	}
	return nil
}

// MarkRunRunning transfers a queued Prompt run to the Worker-owned running
// lease. The lease duration is supplied by the lifecycle boundary, not by a
// task definition or model-provided value.
func (r Repository) MarkRunRunning(ctx context.Context, runID, claimToken string, lease time.Duration) error {
	if r.DB == nil || strings.TrimSpace(runID) == "" || strings.TrimSpace(claimToken) == "" || lease <= 0 {
		return fmt.Errorf("schedule running run identity is invalid")
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE scheduled_task_runs SET state='running',started_at=COALESCE(started_at,?),lease_until=? WHERE id=? AND claim_token=? AND state='queued'`, now, now.Add(lease), runID, claimToken)
	if err != nil {
		return fmt.Errorf("mark scheduled run running: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read scheduled running result: %w", err)
	}
	if count != 1 {
		return ErrRunClaimLost
	}
	return nil
}

func (s RunState) terminal() bool {
	switch s {
	case RunSucceeded, RunFailed, RunCancelled, RunUnknown, RunSkipped:
		return true
	default:
		return false
	}
}
