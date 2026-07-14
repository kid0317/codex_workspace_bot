package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
)

type AttachmentCompletion struct {
	ID, AttemptID, ObservedMIME, SHA256, SessionID, RelativePath string
	ByteSize                                                     int64
	RetentionDeadline                                            time.Time
}

// ExpiredAttachment contains the minimum local filesystem projection needed
// by the retention cleaner. It is never persisted in logs or message bodies.
type ExpiredAttachment struct {
	ID, State, WorkspaceDir, RelativePath, SessionID, OriginalNameSafe string
}

func (s *Store) ListExpiredAttachments(ctx context.Context, limit int) ([]ExpiredAttachment, error) {
	if limit < 1 {
		return nil, fmt.Errorf("expired attachment limit is invalid")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT a.id, a.state, apps.workspace_dir, COALESCE(a.relative_path, ''), a.session_id, a.original_name_safe
		FROM attachments a
		JOIN chat_groups cg ON cg.id=a.chat_group_id
		JOIN apps ON apps.id=cg.app_id
		WHERE a.state IN ('ready','failed')
		  AND a.retention_deadline IS NOT NULL AND a.retention_deadline <= CURRENT_TIMESTAMP(3)
		  AND (a.lease_until IS NULL OR a.lease_until < CURRENT_TIMESTAMP(3))
		ORDER BY a.retention_deadline ASC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list expired attachments: %w", err)
	}
	defer rows.Close()
	var records []ExpiredAttachment
	for rows.Next() {
		var record ExpiredAttachment
		var sessionID, originalNameSafe sql.NullString
		if err := rows.Scan(&record.ID, &record.State, &record.WorkspaceDir, &record.RelativePath, &sessionID, &originalNameSafe); err != nil {
			return nil, fmt.Errorf("scan expired attachment: %w", err)
		}
		record.SessionID = sessionID.String
		record.OriginalNameSafe = originalNameSafe.String
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired attachments: %w", err)
	}
	return records, nil
}

func (s *Store) ClaimAttachmentDeletion(ctx context.Context, attachmentID string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, `UPDATE attachments SET state='deleting', attempt_id=NULL, lease_until=NULL
		WHERE id=? AND state IN ('ready','failed')
		  AND retention_deadline IS NOT NULL AND retention_deadline <= CURRENT_TIMESTAMP(3)
		  AND (lease_until IS NULL OR lease_until < CURRENT_TIMESTAMP(3))`, attachmentID)
	if err != nil {
		return false, fmt.Errorf("claim attachment deletion: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim attachment deletion rows: %w", err)
	}
	return changed == 1, nil
}

func (s *Store) CompleteAttachmentDeletion(ctx context.Context, attachmentID string) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE attachments SET state='deleted', deleted_at=CURRENT_TIMESTAMP(3), lease_until=NULL WHERE id=? AND state='deleting'`, attachmentID)
	if err != nil {
		return fmt.Errorf("complete attachment deletion: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("complete attachment deletion rows: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("complete attachment deletion: invalid state")
	}
	return nil
}

func (s *Store) RestoreAttachmentDeletion(ctx context.Context, attachmentID, state string) error {
	if state != "ready" && state != "failed" {
		return fmt.Errorf("restore attachment deletion: invalid state")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE attachments SET state=?, lease_until=NULL WHERE id=? AND state='deleting'`, state, attachmentID)
	if err != nil {
		return fmt.Errorf("restore attachment deletion: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("restore attachment deletion rows: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("restore attachment deletion: invalid state")
	}
	return nil
}

type ActionCall struct {
	ID, AppID, ChannelKey, ChatGroupID, ThreadID, TurnID, CallID, Tool, ArgumentsDigest string
}

type ActionClaim string

const (
	ActionClaimed  ActionClaim = "claimed"
	ActionConflict ActionClaim = "conflict"
)

type ActionState string

const (
	ActionSucceeded           ActionState = "succeeded"
	ActionRejected            ActionState = "rejected"
	ActionUnknown             ActionState = "unknown"
	ActionCancelledBeforeSend ActionState = "cancelled_before_send"
)

func (s ActionState) terminal() bool {
	return s == ActionSucceeded || s == ActionRejected || s == ActionUnknown || s == ActionCancelledBeforeSend
}

// ClaimAction creates the durable at-most-once boundary before validation or
// any external Feishu call. A separate StartAction transition records the
// point at which the service is permitted to begin an external call.
func (s *Store) ClaimAction(ctx context.Context, call ActionCall) (ActionClaim, error) {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO feishu_action_calls (id,app_id,channel_key,chat_group_id,thread_id,turn_id,call_id,tool,arguments_digest,state) VALUES (?,?,?,?,?,?,?,?,?,'claimed')`, call.ID, call.AppID, call.ChannelKey, call.ChatGroupID, call.ThreadID, call.TurnID, call.CallID, call.Tool, call.ArgumentsDigest)
	if err == nil {
		return ActionClaimed, nil
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return ActionConflict, nil
	}
	return "", fmt.Errorf("claim feishu action: %w", err)
}

// StartAction is the last durable state transition before an external Feishu
// request. It prevents a stale or duplicate claimant from sending anything.
func (s *Store) StartAction(ctx context.Context, appID, threadID, turnID, callID string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, `UPDATE feishu_action_calls SET state='in_flight' WHERE app_id=? AND thread_id=? AND turn_id=? AND call_id=? AND state='claimed'`, appID, threadID, turnID, callID)
	if err != nil {
		return false, fmt.Errorf("start feishu action: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("start feishu action rows: %w", err)
	}
	return changed == 1, nil
}

type ActionResult struct {
	AppID, ThreadID, TurnID, CallID, ObjectDigest string
	ResultEnc                                     []byte
	ResultKeyVersion                              int
	State                                         ActionState
}

type CompletedAction struct {
	Tool, ArgumentsDigest string
	ResultEnc             []byte
	ResultKeyVersion      int
	State                 ActionState
}

func (s *Store) GetCompletedAction(ctx context.Context, appID, threadID, turnID, callID string) (CompletedAction, bool, error) {
	var replay CompletedAction
	err := s.DB.QueryRowContext(ctx, `SELECT tool, arguments_digest, result_enc, result_key_version, state FROM feishu_action_calls WHERE app_id=? AND thread_id=? AND turn_id=? AND call_id=? AND state IN ('succeeded','rejected','unknown','cancelled_before_send')`, appID, threadID, turnID, callID).Scan(&replay.Tool, &replay.ArgumentsDigest, &replay.ResultEnc, &replay.ResultKeyVersion, &replay.State)
	if errors.Is(err, sql.ErrNoRows) {
		return CompletedAction{}, false, nil
	}
	if err != nil {
		return CompletedAction{}, false, fmt.Errorf("get completed feishu action: %w", err)
	}
	return replay, true, nil
}

func (s *Store) CompleteAction(ctx context.Context, result ActionResult) error {
	if result.State == "" {
		result.State = ActionSucceeded
	}
	if !result.State.terminal() {
		return fmt.Errorf("complete feishu action: invalid terminal state")
	}
	execResult, err := s.DB.ExecContext(ctx, `UPDATE feishu_action_calls SET state=?, result_enc=?, result_key_version=?, object_id_digest=?, completed_at=CURRENT_TIMESTAMP(3) WHERE app_id=? AND thread_id=? AND turn_id=? AND call_id=? AND state IN ('claimed','in_flight')`, result.State, result.ResultEnc, result.ResultKeyVersion, nullableString(result.ObjectDigest), result.AppID, result.ThreadID, result.TurnID, result.CallID)
	if err != nil {
		return fmt.Errorf("complete feishu action: %w", err)
	}
	changed, err := execResult.RowsAffected()
	if err != nil {
		return fmt.Errorf("complete feishu action rows: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("complete feishu action: invalid state")
	}
	return nil
}

type AttachmentRecord struct {
	ID, SourceMessageID, OriginalNameSafe, DeclaredMIME string
	Kind                                                AttachmentKind
	SourceResourceRefEnc                                []byte
	SourceRefKeyVersion                                 int
}

func (s *Store) GetAttachmentForWorker(ctx context.Context, attachmentID string) (AttachmentRecord, error) {
	var record AttachmentRecord
	err := s.DB.QueryRowContext(ctx, `SELECT id, kind, source_resource_ref_enc, source_ref_key_version, source_message_id, original_name_safe, declared_mime FROM attachments WHERE id=? AND state IN ('staged','processing')`, attachmentID).Scan(&record.ID, &record.Kind, &record.SourceResourceRefEnc, &record.SourceRefKeyVersion, &record.SourceMessageID, &record.OriginalNameSafe, &record.DeclaredMIME)
	if err != nil {
		return AttachmentRecord{}, fmt.Errorf("get attachment for worker: %w", err)
	}
	return record, nil
}

// ClaimAttachment transitions only a newly staged attachment. A false result
// means a duplicate worker or cleanup process already owns/finished it.
func (s *Store) ClaimAttachment(ctx context.Context, attachmentID, attemptID string, leaseUntil time.Time) (bool, error) {
	result, err := s.DB.ExecContext(ctx, `UPDATE attachments SET state='processing', attempt_id=?, lease_until=?, error_code=NULL WHERE id=? AND state='staged'`, attemptID, leaseUntil, attachmentID)
	if err != nil {
		return false, fmt.Errorf("claim attachment: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim attachment rows: %w", err)
	}
	return changed == 1, nil
}

func (s *Store) CompleteAttachment(ctx context.Context, completion AttachmentCompletion) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE attachments SET state='ready', observed_mime=?, byte_size=?, sha256=?, session_id=?, relative_path=?, retention_deadline=?, source_resource_ref_enc=NULL, source_ref_key_version=NULL, lease_until=NULL WHERE id=? AND attempt_id=? AND state='processing'`, completion.ObservedMIME, completion.ByteSize, completion.SHA256, completion.SessionID, completion.RelativePath, completion.RetentionDeadline, completion.ID, completion.AttemptID)
	if err != nil {
		return fmt.Errorf("complete attachment: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("complete attachment rows: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("complete attachment: invalid state")
	}
	return nil
}

func (s *Store) FailAttachment(ctx context.Context, attachmentID, attemptID, code string, retentionDeadline time.Time) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE attachments SET state='failed', error_code=?, retention_deadline=?, source_resource_ref_enc=NULL, source_ref_key_version=NULL, lease_until=NULL WHERE id=? AND attempt_id=? AND state='processing'`, code, retentionDeadline, attachmentID, attemptID)
	if err != nil {
		return fmt.Errorf("fail attachment: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("fail attachment rows: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("fail attachment: invalid state")
	}
	return nil
}

// ReconcileInterruptedAttachments is the restart boundary. A new bot process
// never replays queued or in-flight attachment downloads from a previous
// generation, regardless of their old lease timestamp.
func (s *Store) ReconcileInterruptedAttachments(ctx context.Context, retentionDeadline time.Time) (int64, error) {
	if retentionDeadline.IsZero() {
		return 0, fmt.Errorf("reconcile interrupted attachments: retention deadline is required")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE attachments SET state='failed', error_code='attachment_interrupted', retention_deadline=COALESCE(retention_deadline, ?), lease_until=NULL, source_resource_ref_enc=NULL, source_ref_key_version=NULL WHERE state IN ('staged','processing')`, retentionDeadline)
	if err != nil {
		return 0, fmt.Errorf("reconcile abandoned attachments: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("reconcile abandoned attachments rows: %w", err)
	}
	return count, nil
}
