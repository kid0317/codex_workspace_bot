package schedule

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
)

// ToolCallIdentity is the exact App Server request identity. The route fields
// are included so a ciphertext cannot be replayed into another channel even
// if an App Server happened to reuse call IDs.
type ToolCallIdentity struct {
	AppID, ChannelKey, ChatGroupID string
	ThreadID, TurnID, CallID, Tool string
}

type ToolCallState string

const (
	ToolCallClaimed   ToolCallState = "claimed"
	ToolCallInFlight  ToolCallState = "in_flight"
	ToolCallSucceeded ToolCallState = "succeeded"
	ToolCallRejected  ToolCallState = "rejected"
)

type ToolCallInput struct {
	ID            string
	Identity      ToolCallIdentity
	ArgumentsHMAC string
	Lease         time.Duration
}

type ToolCallClaimKind string

const (
	ToolCallNew      ToolCallClaimKind = "new"
	ToolCallReplay   ToolCallClaimKind = "replay"
	ToolCallConflict ToolCallClaimKind = "conflict"
	ToolCallBusy     ToolCallClaimKind = "busy"
)

type ToolCallReplayRecord struct {
	Payload   []byte
	State     ToolCallState
	ErrorCode string
}

type ToolCallClaim struct {
	Kind   ToolCallClaimKind
	Replay ToolCallReplayRecord
}

var (
	ErrToolCallConflict = errors.New("schedule tool call arguments conflict")
	ErrToolCallBusy     = errors.New("schedule tool call is already in flight")
)

// ToolCallExecution is the decrypted, tool-safe terminal result returned to
// the App Server adapter. The durable plaintext copy is written in the same transaction as the
// task mutation that produced it.
type ToolCallExecution struct {
	Payload   []byte
	Success   bool
	ErrorCode string
	Replay    bool
}

// CreateTaskForToolCall couples the durable duplicate gate, task insertion,
// and encrypted terminal result in a single transaction. A committed success
// can therefore always be replayed without creating a second task.
func (r Repository) CreateTaskForToolCall(ctx context.Context, input ToolCallInput, draft TaskDraft, encode func(Task) ([]byte, error)) (ToolCallExecution, error) {
	if r.DB == nil {
		return ToolCallExecution{}, fmt.Errorf("schedule store database is required")
	}
	if encode == nil {
		return ToolCallExecution{}, fmt.Errorf("schedule tool result encoder is required")
	}
	if err := input.Identity.validate(); err != nil || strings.TrimSpace(input.ArgumentsHMAC) == "" {
		return ToolCallExecution{}, fmt.Errorf("schedule tool call input is invalid")
	}
	if input.Lease <= 0 {
		input.Lease = 30 * time.Second
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	if strings.TrimSpace(input.ID) == "" {
		newID := r.NewID
		if newID == nil {
			newID = uuid.NewString
		}
		input.ID = newID()
	}
	if strings.TrimSpace(input.ID) == "" {
		return ToolCallExecution{}, fmt.Errorf("schedule tool call id is required")
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return ToolCallExecution{}, fmt.Errorf("begin schedule tool call: %w", err)
	}
	defer tx.Rollback()

	inserted, replay, err := r.claimToolCallTx(ctx, tx, input, now)
	if err != nil {
		return ToolCallExecution{}, err
	}
	if !inserted {
		if err := tx.Commit(); err != nil {
			return ToolCallExecution{}, fmt.Errorf("commit schedule tool replay: %w", err)
		}
		return replay, nil
	}
	if err := r.checkCreateQuotaTx(ctx, tx, draft.Owner); err != nil {
		return ToolCallExecution{}, err
	}
	task, err := r.createTaskExec(ctx, tx, draft)
	if err != nil {
		return ToolCallExecution{}, err
	}
	payload, err := encode(task)
	if err != nil {
		return ToolCallExecution{}, fmt.Errorf("encode schedule tool result: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_task_tool_calls SET state='succeeded',result_text=?,result_enc=NULL,result_key_version=NULL,lease_until=NULL,completed_at=? WHERE app_id=? AND thread_id=? AND turn_id=? AND call_id=? AND state='claimed'`, string(payload), now, input.Identity.AppID, input.Identity.ThreadID, input.Identity.TurnID, input.Identity.CallID)
	if err != nil {
		return ToolCallExecution{}, fmt.Errorf("complete schedule tool call: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ToolCallExecution{}, fmt.Errorf("read schedule tool completion: %w", err)
	}
	if changed != 1 {
		return ToolCallExecution{}, ErrToolCallBusy
	}
	if err := tx.Commit(); err != nil {
		return ToolCallExecution{}, fmt.Errorf("commit schedule tool call: %w", err)
	}
	return ToolCallExecution{Payload: payload, Success: true}, nil
}

// ListOwnedTasksForToolCall commits the exact owner-scoped page returned for a
// call ID. It shares the encrypted result/replay contract with mutations even
// though the underlying task rows are read-only.
func (r Repository) ListOwnedTasksForToolCall(ctx context.Context, input ToolCallInput, owner Owner, after *CursorPosition, pageSize int, encode func(TaskPage) ([]byte, error)) (ToolCallExecution, error) {
	if r.DB == nil {
		return ToolCallExecution{}, fmt.Errorf("schedule store database is required")
	}
	if encode == nil {
		return ToolCallExecution{}, fmt.Errorf("schedule tool result encoder is required")
	}
	if err := input.Identity.validate(); err != nil || strings.TrimSpace(input.ArgumentsHMAC) == "" {
		return ToolCallExecution{}, fmt.Errorf("schedule tool call input is invalid")
	}
	if input.Lease <= 0 {
		input.Lease = 30 * time.Second
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	if strings.TrimSpace(input.ID) == "" {
		newID := r.NewID
		if newID == nil {
			newID = uuid.NewString
		}
		input.ID = newID()
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return ToolCallExecution{}, fmt.Errorf("begin schedule tool list: %w", err)
	}
	defer tx.Rollback()
	inserted, replay, err := r.claimToolCallTx(ctx, tx, input, now)
	if err != nil {
		return ToolCallExecution{}, err
	}
	if !inserted {
		if err := tx.Commit(); err != nil {
			return ToolCallExecution{}, fmt.Errorf("commit schedule tool replay: %w", err)
		}
		return replay, nil
	}
	page, err := r.listOwnedTasksQuery(ctx, tx, owner, after, pageSize)
	if err != nil {
		return ToolCallExecution{}, err
	}
	payload, err := encode(page)
	if err != nil {
		return ToolCallExecution{}, fmt.Errorf("encode schedule tool result: %w", err)
	}
	if err := r.completeToolCallTx(ctx, tx, input.Identity, payload, now); err != nil {
		return ToolCallExecution{}, err
	}
	if err := tx.Commit(); err != nil {
		return ToolCallExecution{}, fmt.Errorf("commit schedule tool list: %w", err)
	}
	return ToolCallExecution{Payload: payload, Success: true}, nil
}

// UpdateTaskForToolCall keeps the owner/version CAS and terminal call result
// in one transaction. A duplicate completed call therefore returns the exact
// response from the mutation it originally authorized.
func (r Repository) UpdateTaskForToolCall(ctx context.Context, input ToolCallInput, patch TaskPatch, encode func(Task) ([]byte, error)) (ToolCallExecution, error) {
	if r.DB == nil {
		return ToolCallExecution{}, fmt.Errorf("schedule store database is required")
	}
	if encode == nil {
		return ToolCallExecution{}, fmt.Errorf("schedule tool result encoder is required")
	}
	if err := input.Identity.validate(); err != nil || strings.TrimSpace(input.ArgumentsHMAC) == "" {
		return ToolCallExecution{}, fmt.Errorf("schedule tool call input is invalid")
	}
	if input.Lease <= 0 {
		input.Lease = 30 * time.Second
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	if strings.TrimSpace(input.ID) == "" {
		newID := r.NewID
		if newID == nil {
			newID = uuid.NewString
		}
		input.ID = newID()
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return ToolCallExecution{}, fmt.Errorf("begin schedule tool update: %w", err)
	}
	defer tx.Rollback()
	inserted, replay, err := r.claimToolCallTx(ctx, tx, input, now)
	if err != nil {
		return ToolCallExecution{}, err
	}
	if !inserted {
		if err := tx.Commit(); err != nil {
			return ToolCallExecution{}, fmt.Errorf("commit schedule tool replay: %w", err)
		}
		return replay, nil
	}
	task, err := r.updateTaskTx(ctx, tx, patch)
	if err != nil {
		return ToolCallExecution{}, err
	}
	payload, err := encode(task)
	if err != nil {
		return ToolCallExecution{}, fmt.Errorf("encode schedule tool result: %w", err)
	}
	if err := r.completeToolCallTx(ctx, tx, input.Identity, payload, now); err != nil {
		return ToolCallExecution{}, err
	}
	if err := tx.Commit(); err != nil {
		return ToolCallExecution{}, fmt.Errorf("commit schedule tool update: %w", err)
	}
	return ToolCallExecution{Payload: payload, Success: true}, nil
}

func (r Repository) completeToolCallTx(ctx context.Context, tx *sql.Tx, identity ToolCallIdentity, payload []byte, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_task_tool_calls SET state='succeeded',result_text=?,result_enc=NULL,result_key_version=NULL,lease_until=NULL,completed_at=? WHERE app_id=? AND thread_id=? AND turn_id=? AND call_id=? AND state='claimed'`, string(payload), now, identity.AppID, identity.ThreadID, identity.TurnID, identity.CallID)
	if err != nil {
		return fmt.Errorf("complete schedule tool call: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read schedule tool completion: %w", err)
	}
	if changed != 1 {
		return ErrToolCallBusy
	}
	return nil
}

func (r Repository) claimToolCallTx(ctx context.Context, tx *sql.Tx, input ToolCallInput, now time.Time) (bool, ToolCallExecution, error) {
	_, err := tx.ExecContext(ctx, `INSERT INTO scheduled_task_tool_calls (id,app_id,channel_key,chat_group_id,thread_id,turn_id,call_id,tool,arguments_hmac,state,lease_until) VALUES (?,?,?,?,?,?,?,?,?,'claimed',?)`, input.ID, input.Identity.AppID, input.Identity.ChannelKey, input.Identity.ChatGroupID, input.Identity.ThreadID, input.Identity.TurnID, input.Identity.CallID, input.Identity.Tool, input.ArgumentsHMAC, now.Add(input.Lease))
	if err == nil {
		return true, ToolCallExecution{}, nil
	}
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
		return false, ToolCallExecution{}, fmt.Errorf("claim schedule tool call: %w", err)
	}
	var stored struct {
		arguments string
		state     ToolCallState
		result    sql.NullString
		errorCode sql.NullString
	}
	err = tx.QueryRowContext(ctx, `SELECT arguments_hmac,state,result_text,error_code FROM scheduled_task_tool_calls WHERE app_id=? AND thread_id=? AND turn_id=? AND call_id=? FOR UPDATE`, input.Identity.AppID, input.Identity.ThreadID, input.Identity.TurnID, input.Identity.CallID).Scan(&stored.arguments, &stored.state, &stored.result, &stored.errorCode)
	if err != nil {
		return false, ToolCallExecution{}, fmt.Errorf("load claimed schedule tool call: %w", err)
	}
	if stored.arguments != input.ArgumentsHMAC {
		return false, ToolCallExecution{}, ErrToolCallConflict
	}
	if stored.state == ToolCallSucceeded {
		return false, ToolCallExecution{Payload: []byte(stored.result.String), Success: true, Replay: true}, nil
	}
	if stored.state == ToolCallRejected {
		return false, ToolCallExecution{Success: false, ErrorCode: stored.errorCode.String, Replay: true}, nil
	}
	return false, ToolCallExecution{}, ErrToolCallBusy
}

func (i ToolCallIdentity) validate() error {
	if strings.TrimSpace(i.AppID) == "" || strings.TrimSpace(i.ChannelKey) == "" || strings.TrimSpace(i.ChatGroupID) == "" || strings.TrimSpace(i.ThreadID) == "" || strings.TrimSpace(i.TurnID) == "" || strings.TrimSpace(i.CallID) == "" || strings.TrimSpace(i.Tool) == "" {
		return fmt.Errorf("schedule tool call identity is incomplete")
	}
	return nil
}

// ClaimToolCall provides the durable duplicate gate. It is deliberately not
// the final public mutation API: create/update/list will call its transactional
// successor so a task mutation and encrypted result share one commit.
func (r Repository) ClaimToolCall(ctx context.Context, input ToolCallInput) (ToolCallClaim, error) {
	if r.DB == nil {
		return ToolCallClaim{}, fmt.Errorf("schedule store database is required")
	}
	if err := input.Identity.validate(); err != nil || strings.TrimSpace(input.ArgumentsHMAC) == "" {
		return ToolCallClaim{}, fmt.Errorf("schedule tool call input is invalid")
	}
	if input.Lease <= 0 {
		input.Lease = 30 * time.Second
	}
	newID := r.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	if strings.TrimSpace(input.ID) == "" {
		input.ID = newID()
	}
	if strings.TrimSpace(input.ID) == "" {
		return ToolCallClaim{}, fmt.Errorf("schedule tool call id is required")
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO scheduled_task_tool_calls (id,app_id,channel_key,chat_group_id,thread_id,turn_id,call_id,tool,arguments_hmac,state,lease_until) VALUES (?,?,?,?,?,?,?,?,?,'claimed',?)`, input.ID, input.Identity.AppID, input.Identity.ChannelKey, input.Identity.ChatGroupID, input.Identity.ThreadID, input.Identity.TurnID, input.Identity.CallID, input.Identity.Tool, input.ArgumentsHMAC, now.Add(input.Lease))
	if err == nil {
		return ToolCallClaim{Kind: ToolCallNew}, nil
	}
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
		return ToolCallClaim{}, fmt.Errorf("claim schedule tool call: %w", err)
	}
	var stored struct {
		arguments string
		state     ToolCallState
		result    sql.NullString
		errorCode sql.NullString
		lease     sql.NullTime
	}
	err = r.DB.QueryRowContext(ctx, `SELECT arguments_hmac,state,result_text,error_code,lease_until FROM scheduled_task_tool_calls WHERE app_id=? AND thread_id=? AND turn_id=? AND call_id=?`, input.Identity.AppID, input.Identity.ThreadID, input.Identity.TurnID, input.Identity.CallID).Scan(&stored.arguments, &stored.state, &stored.result, &stored.errorCode, &stored.lease)
	if err != nil {
		return ToolCallClaim{}, fmt.Errorf("load claimed schedule tool call: %w", err)
	}
	if stored.arguments != input.ArgumentsHMAC {
		return ToolCallClaim{Kind: ToolCallConflict}, nil
	}
	if stored.state == ToolCallSucceeded || stored.state == ToolCallRejected {
		return ToolCallClaim{Kind: ToolCallReplay, Replay: ToolCallReplayRecord{Payload: []byte(stored.result.String), State: stored.state, ErrorCode: stored.errorCode.String}}, nil
	}
	return ToolCallClaim{Kind: ToolCallBusy}, nil
}

// ReconcileExpiredToolCalls refuses to replay an unfinished request: there is
// no reliable way to determine whether its transaction committed after a
// process crash. The next request gets a stable rejection instead.
func (r Repository) ReconcileExpiredToolCalls(ctx context.Context, now time.Time) (int64, error) {
	if r.DB == nil {
		return 0, fmt.Errorf("schedule store database is required")
	}
	if now.IsZero() {
		return 0, fmt.Errorf("schedule tool-call reconcile time is required")
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE scheduled_task_tool_calls SET state='rejected',error_code='tool_call_lease_expired',lease_until=NULL,completed_at=? WHERE state IN ('claimed','in_flight') AND lease_until < ?`, now.UTC(), now.UTC())
	if err != nil {
		return 0, fmt.Errorf("reconcile expired schedule tool calls: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read expired schedule tool call reconciliation result: %w", err)
	}
	return count, nil
}
