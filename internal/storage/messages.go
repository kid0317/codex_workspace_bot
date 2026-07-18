package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

type MessageInput struct {
	ID                   string
	TraceID              string
	ChatGroupID          string
	FeishuEventID        string
	FeishuUserMessageID  string
	SenderOpenID         string
	UserContent          string
	ReceivedAt           time.Time
	CommandKind          string
	CommandPayloadSHA256 string
	CommandPayloadBytes  int
	Attachments          []AttachmentInput
}

type AttachmentKind string

const (
	AttachmentImage AttachmentKind = "image"
	AttachmentFile  AttachmentKind = "file"
)

// AttachmentInput is the immutable ingress record staged before a worker
// downloads the resource into its session-scoped attachment directory.
type AttachmentInput struct {
	ID                   string
	Kind                 AttachmentKind
	SourceResourceRefEnc []byte
	SourceRefKeyVersion  int
	SourceMessageID      string
	OriginalNameSafe     string
	DeclaredMIME         string
}

type MessageRecord struct {
	ID          string
	TraceID     string
	ChatGroupID string
}

const persistIncomingMessageSQL = `INSERT INTO messages (id,trace_id,chat_group_id,feishu_event_id,feishu_user_message_id,sender_open_id,user_content,received_at,command_kind,command_payload_sha256,command_payload_bytes,command_effect_state,command_reply_outcome,status) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,'received')`

type CompanionDeliverySummary struct {
	BatchID, FirstMessageID, Content, ErrorCode string
	DurationMS                                  int64
}

func (s *Store) GetOrCreateChatGroup(ctx context.Context, appID, chatType, chatID string) (string, error) {
	const insert = `INSERT INTO chat_groups (id, app_id, chat_type, chat_id) VALUES (UUID(), ?, ?, ?)
		ON DUPLICATE KEY UPDATE last_message_at = CURRENT_TIMESTAMP(3)`
	if _, err := s.DB.ExecContext(ctx, insert, appID, chatType, chatID); err != nil {
		return "", fmt.Errorf("upsert chat group: %w", err)
	}
	const selectID = `SELECT id FROM chat_groups WHERE app_id = ? AND chat_type = ? AND chat_id = ?`
	var id string
	if err := s.DB.QueryRowContext(ctx, selectID, appID, chatType, chatID).Scan(&id); err != nil {
		return "", fmt.Errorf("select chat group: %w", err)
	}
	return id, nil
}

func (s *Store) GetChatGroupThread(ctx context.Context, chatGroupID string) (string, error) {
	var threadID *string
	if err := s.DB.QueryRowContext(ctx, `SELECT codex_thread_id FROM chat_groups WHERE id=?`, chatGroupID).Scan(&threadID); err != nil {
		return "", fmt.Errorf("get chat group thread: %w", err)
	}
	if threadID == nil {
		return "", nil
	}
	return *threadID, nil
}

func (s *Store) GetChatGroupToolset(ctx context.Context, chatGroupID string) (string, error) {
	var version *string
	if err := s.DB.QueryRowContext(ctx, `SELECT codex_toolset_version FROM chat_groups WHERE id=?`, chatGroupID).Scan(&version); err != nil {
		return "", fmt.Errorf("get chat group toolset: %w", err)
	}
	if version == nil {
		return "", nil
	}
	return *version, nil
}

func (s *Store) SetChatGroupToolset(ctx context.Context, chatGroupID, version string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE chat_groups SET codex_toolset_version=?, updated_at=CURRENT_TIMESTAMP(3) WHERE id=?`, nullableString(version), chatGroupID)
	if err != nil {
		return fmt.Errorf("set chat group toolset: %w", err)
	}
	return nil
}

func (s *Store) SetThreadIfExpected(ctx context.Context, chatGroupID, expected, replacement string) (bool, error) {
	query := `UPDATE chat_groups SET codex_thread_id=?, updated_at=CURRENT_TIMESTAMP(3) WHERE id=? AND codex_thread_id <=> ?`
	result, err := s.DB.ExecContext(ctx, query, nullableThread(replacement), chatGroupID, nullableThread(expected))
	if err != nil {
		return false, fmt.Errorf("set chat group thread: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("set chat group thread rows: %w", err)
	}
	return changed == 1, nil
}

func nullableThread(threadID string) any {
	if threadID == "" {
		return nil
	}
	return threadID
}

func (s *Store) PersistIncoming(ctx context.Context, appID, chatType, chatID string, input MessageInput) (MessageRecord, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return MessageRecord{}, false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO chat_groups (id, app_id, chat_type, chat_id) VALUES (UUID(), ?, ?, ?) ON DUPLICATE KEY UPDATE last_message_at=CURRENT_TIMESTAMP(3)`, appID, chatType, chatID); err != nil {
		return MessageRecord{}, false, err
	}
	var groupID string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM chat_groups WHERE app_id=? AND chat_type=? AND chat_id=?`, appID, chatType, chatID).Scan(&groupID); err != nil {
		return MessageRecord{}, false, err
	}
	input.ChatGroupID = groupID
	if input.ReceivedAt.IsZero() {
		input.ReceivedAt = time.Now().UTC()
	}
	commandKind := nullableString(input.CommandKind)
	commandDigest := nullableString(input.CommandPayloadSHA256)
	var commandBytes any
	if input.CommandPayloadBytes > 0 {
		commandBytes = input.CommandPayloadBytes
	}
	commandEffect, commandReply := any(nil), any(nil)
	if input.CommandKind != "" {
		commandEffect, commandReply = "pending", "pending"
	}
	_, err = tx.ExecContext(ctx, persistIncomingMessageSQL, input.ID, input.TraceID, input.ChatGroupID, input.FeishuEventID, input.FeishuUserMessageID, input.SenderOpenID, input.UserContent, input.ReceivedAt.UTC(), commandKind, commandDigest, commandBytes, commandEffect, commandReply)
	if err != nil {
		var e *mysql.MySQLError
		if errors.As(err, &e) && e.Number == 1062 {
			return MessageRecord{}, true, nil
		}
		return MessageRecord{}, false, err
	}
	for _, attachment := range input.Attachments {
		_, err = tx.ExecContext(ctx, `INSERT INTO attachments (id,message_id,chat_group_id,kind,source_resource_ref_enc,source_ref_key_version,source_message_id,original_name_safe,declared_mime,state) VALUES (?,?,?,?,?,?,?,?,?,'staged')`, attachment.ID, input.ID, input.ChatGroupID, attachment.Kind, attachment.SourceResourceRefEnc, attachment.SourceRefKeyVersion, attachment.SourceMessageID, attachment.OriginalNameSafe, attachment.DeclaredMIME)
		if err != nil {
			return MessageRecord{}, false, fmt.Errorf("stage attachment: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return MessageRecord{}, false, err
	}
	return MessageRecord{ID: input.ID, TraceID: input.TraceID, ChatGroupID: groupID}, false, nil
}

func (s *Store) CreateMessage(ctx context.Context, input MessageInput) (MessageRecord, bool, error) {
	const query = `INSERT INTO messages (id, trace_id, chat_group_id, feishu_event_id, feishu_user_message_id, sender_open_id, user_content, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'received')`
	_, err := s.DB.ExecContext(ctx, query, input.ID, input.TraceID, input.ChatGroupID, input.FeishuEventID, input.FeishuUserMessageID, input.SenderOpenID, input.UserContent)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return MessageRecord{}, true, nil
		}
		return MessageRecord{}, false, fmt.Errorf("insert message: %w", err)
	}
	return MessageRecord{ID: input.ID, TraceID: input.TraceID}, false, nil
}

func (s *Store) CompleteMessage(ctx context.Context, id, botMessageID, content string, durationMS int64) error {
	const query = `UPDATE messages SET status = 'succeeded', feishu_bot_message_id = ?, assistant_content = ?, duration_ms = ?, completed_at = CURRENT_TIMESTAMP(3), command_effect_state=CASE WHEN command_kind IS NULL THEN command_effect_state ELSE 'succeeded' END, command_reply_outcome=CASE WHEN command_kind IS NULL THEN command_reply_outcome WHEN ? = '' THEN 'rejected' ELSE 'sent' END WHERE id = ?`
	if _, err := s.DB.ExecContext(ctx, query, botMessageID, content, durationMS, botMessageID, id); err != nil {
		return fmt.Errorf("complete message: %w", err)
	}
	return nil
}

func (s *Store) FailMessage(ctx context.Context, id, errorCode, safeError string, durationMS int64) error {
	const query = `UPDATE messages SET status = 'failed', error_code = ?, assistant_content = ?, duration_ms = ?, completed_at = CURRENT_TIMESTAMP(3), command_effect_state=CASE WHEN command_kind IS NULL THEN command_effect_state WHEN ? IN ('command_reply_unknown','command_reply_rejected') THEN 'succeeded' ELSE 'failed' END, command_reply_outcome=CASE WHEN command_kind IS NULL THEN command_reply_outcome WHEN ? = 'command_reply_unknown' THEN 'unknown' WHEN ? = 'command_reply_rejected' THEN 'rejected' ELSE 'sent' END WHERE id = ?`
	if _, err := s.DB.ExecContext(ctx, query, errorCode, safeError, durationMS, errorCode, errorCode, errorCode, id); err != nil {
		return fmt.Errorf("fail message: %w", err)
	}
	return nil
}

// RecordCommandReplyFailure keeps a command effect separate from a failed or
// indeterminate confirmation delivery.
func (s *Store) RecordCommandReplyFailure(ctx context.Context, id, effect, outcome, safeError string) error {
	if effect != "succeeded" && effect != "failed" || outcome != "unknown" && outcome != "rejected" {
		return fmt.Errorf("invalid command reply outcome")
	}
	code := "command_reply_" + outcome
	_, err := s.DB.ExecContext(ctx, `UPDATE messages SET status='failed', error_code=?, assistant_content=?, completed_at=CURRENT_TIMESTAMP(3), command_effect_state=?, command_reply_outcome=? WHERE id=? AND command_kind IS NOT NULL`, code, safeError, effect, outcome, id)
	if err != nil {
		return fmt.Errorf("record command reply failure: %w", err)
	}
	return nil
}

func (s *Store) MarkMessagesProcessing(ctx context.Context, ids []string) error {
	for _, id := range ids {
		result, err := s.DB.ExecContext(ctx, `UPDATE messages SET status='processing' WHERE id=? AND status='received'`, id)
		if err != nil {
			return fmt.Errorf("mark message processing: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return fmt.Errorf("mark message processing: invalid state")
		}
	}
	return nil
}

func (s *Store) CompleteBatch(ctx context.Context, ids []string, cardID, content string, durationMS int64) error {
	if len(ids) == 0 {
		return fmt.Errorf("complete batch: ids are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin complete batch: %w", err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE messages SET status='succeeded', feishu_bot_message_id=?, assistant_content=?, duration_ms=?, completed_at=CURRENT_TIMESTAMP(3) WHERE id=? AND status='processing'`, cardID, content, durationMS, id)
		if err != nil {
			return fmt.Errorf("complete batch message: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return fmt.Errorf("complete batch message: invalid state")
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit complete batch: %w", err)
	}
	return nil
}

func (s *Store) MarkCompanionDeliveryStarted(ctx context.Context, ids []string, batchID string) error {
	if len(ids) == 0 || batchID == "" {
		return fmt.Errorf("mark companion delivery started: ids and batch id are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin companion delivery marker: %w", err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE messages SET companion_delivery_batch_id=?, delivery_stage='companion_delivery_started' WHERE id=? AND status='processing' AND companion_delivery_batch_id IS NULL AND delivery_stage IS NULL`, batchID, id)
		if err != nil {
			return fmt.Errorf("mark companion delivery message: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return fmt.Errorf("mark companion delivery message: invalid state")
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit companion delivery marker: %w", err)
	}
	return nil
}

func (s *Store) FailCompanionDelivery(ctx context.Context, ids []string, batchID, code, safeContent string, durationMS int64) error {
	if len(ids) == 0 || batchID == "" || code == "" {
		return fmt.Errorf("fail companion delivery: ids, batch id, and code are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin companion delivery failure: %w", err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE messages SET status='failed', error_code=?, assistant_content=?, duration_ms=?, completed_at=CURRENT_TIMESTAMP(3), delivery_stage=NULL WHERE companion_delivery_batch_id=? AND id=? AND status='processing' AND delivery_stage='companion_delivery_started'`, code, safeContent, durationMS, batchID, id)
		if err != nil {
			return fmt.Errorf("fail companion delivery message: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return fmt.Errorf("fail companion delivery message: invalid state")
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit companion delivery failure: %w", err)
	}
	return nil
}

func (s *Store) CompleteCompanionDelivery(ctx context.Context, ids []string, summary CompanionDeliverySummary) error {
	if len(ids) == 0 || summary.BatchID == "" {
		return fmt.Errorf("complete companion delivery: ids and batch id are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin companion delivery completion: %w", err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE messages SET status='succeeded', feishu_bot_message_id=?, assistant_content=?, duration_ms=?, error_code=?, completed_at=CURRENT_TIMESTAMP(3), delivery_stage=NULL WHERE companion_delivery_batch_id=? AND id=? AND status='processing' AND delivery_stage='companion_delivery_started'`, summary.FirstMessageID, summary.Content, summary.DurationMS, nullableString(summary.ErrorCode), summary.BatchID, id)
		if err != nil {
			return fmt.Errorf("complete companion delivery message: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return fmt.Errorf("complete companion delivery message: invalid state")
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit companion delivery completion: %w", err)
	}
	return nil
}

// ReconcileAbandonedCompanionDeliveries implements the user-selected restart
// policy: never resend a possibly visible segment. Only batches that crossed
// the durable delivery-start marker are moved to a deterministic failed state.
func (s *Store) ReconcileAbandonedCompanionDeliveries(ctx context.Context) (int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT companion_delivery_batch_id FROM messages WHERE status='processing' AND delivery_stage='companion_delivery_started' AND companion_delivery_batch_id IS NOT NULL`)
	if err != nil {
		return 0, fmt.Errorf("query abandoned companion deliveries: %w", err)
	}
	defer rows.Close()
	var batchIDs []string
	for rows.Next() {
		var batchID string
		if err := rows.Scan(&batchID); err != nil {
			return 0, fmt.Errorf("scan abandoned companion delivery: %w", err)
		}
		batchIDs = append(batchIDs, batchID)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate abandoned companion deliveries: %w", err)
	}
	for _, batchID := range batchIDs {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return 0, fmt.Errorf("begin abandoned companion delivery reconciliation: %w", err)
		}
		result, execErr := tx.ExecContext(ctx, `UPDATE messages SET status='failed', error_code='companion_delivery_abandoned', assistant_content='本批处理在发送过程中中断，请重新发送。', completed_at=CURRENT_TIMESTAMP(3), delivery_stage=NULL WHERE status='processing' AND delivery_stage='companion_delivery_started' AND companion_delivery_batch_id=?`, batchID)
		if execErr != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("reconcile abandoned companion delivery: %w", execErr)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed == 0 {
			_ = tx.Rollback()
			if err != nil {
				return 0, fmt.Errorf("count abandoned companion delivery: %w", err)
			}
			return 0, fmt.Errorf("reconcile abandoned companion delivery: invalid state")
		}
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("commit abandoned companion delivery reconciliation: %w", err)
		}
	}
	return len(batchIDs), nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) FailBatch(ctx context.Context, ids []string, code string, durationMS int64) error {
	if len(ids) == 0 {
		return fmt.Errorf("fail batch: ids are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fail batch: %w", err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE messages SET status='failed', error_code=?, assistant_content='本批处理失败，请重新发送。', duration_ms=?, completed_at=CURRENT_TIMESTAMP(3) WHERE id=? AND status IN ('received','processing')`, code, durationMS, id)
		if err != nil {
			return fmt.Errorf("fail batch message: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return fmt.Errorf("fail batch message: invalid state")
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fail batch: %w", err)
	}
	return nil
}

type BatchLifecycle struct{ Store *Store }

func (l BatchLifecycle) MarkProcessing(ctx context.Context, ids []string) error {
	return l.Store.MarkMessagesProcessing(ctx, ids)
}
func (l BatchLifecycle) Complete(ctx context.Context, ids []string, cardID, content string, durationMS int64) error {
	return l.Store.CompleteBatch(ctx, ids, cardID, content, durationMS)
}
func (l BatchLifecycle) Fail(ctx context.Context, ids []string, code string, durationMS int64) error {
	return l.Store.FailBatch(ctx, ids, code, durationMS)
}
func (l BatchLifecycle) MarkCompanionDeliveryStarted(ctx context.Context, ids []string, batchID string) error {
	return l.Store.MarkCompanionDeliveryStarted(ctx, ids, batchID)
}
func (l BatchLifecycle) CompleteCompanionDelivery(ctx context.Context, ids []string, summary worker.CompanionDeliverySummary) error {
	return l.Store.CompleteCompanionDelivery(ctx, ids, CompanionDeliverySummary{BatchID: summary.BatchID, FirstMessageID: summary.FirstMessageID, Content: summary.Content, ErrorCode: summary.ErrorCode, DurationMS: summary.DurationMS})
}
func (l BatchLifecycle) FailCompanionDelivery(ctx context.Context, ids []string, batchID, code, safeContent string, durationMS int64) error {
	return l.Store.FailCompanionDelivery(ctx, ids, batchID, code, safeContent, durationMS)
}

func (l BatchLifecycle) MarkScheduledRunning(ctx context.Context, runID, claimToken string) error {
	return l.Store.MarkScheduledRunning(ctx, runID, claimToken)
}

func (l BatchLifecycle) CompleteScheduledRun(ctx context.Context, runID, claimToken string, succeeded bool, code string, durationMS int64) error {
	return l.Store.CompleteScheduledRun(ctx, runID, claimToken, succeeded, code, durationMS)
}
