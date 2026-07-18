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

type DeliveryKind string
type DeliveryStage string
type DeliveryOutcome string

const (
	DeliveryResultCard   DeliveryKind    = "result_card"
	DeliveryFallbackText DeliveryKind    = "fallback_text"
	DeliveryPending      DeliveryStage   = "pending"
	DeliveryInFlight     DeliveryStage   = "in_flight"
	DeliverySent         DeliveryOutcome = "sent"
	DeliveryRejected     DeliveryOutcome = "rejected"
	DeliveryUnknown      DeliveryOutcome = "unknown"
	DeliverySuppressed   DeliveryOutcome = "suppressed"
)

// DeliveryIntent is metadata only. It never carries result text, route IDs,
// console output, or the raw Feishu message ID.
type DeliveryIntent struct {
	ID, RunID string
	Kind      DeliveryKind
	Attempt   int
	Stage     DeliveryStage
	Outcome   DeliveryOutcome
}

// CreateResultDelivery creates the one primary result delivery for a terminal
// run. A silent task is terminally suppressed immediately; it never enters
// pending/in_flight and therefore cannot be accidentally sent by a retry.
func (r Repository) CreateResultDelivery(ctx context.Context, runID string, _ bool) (DeliveryIntent, error) {
	if r.DB == nil || strings.TrimSpace(runID) == "" {
		return DeliveryIntent{}, fmt.Errorf("schedule result delivery identity is invalid")
	}
	newID := r.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	id := newID()
	if strings.TrimSpace(id) == "" {
		return DeliveryIntent{}, fmt.Errorf("schedule delivery id is required")
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	// The silent flag comes from the immutable run snapshot, not from the
	// caller. The SELECT also makes a delivery impossible for a non-terminal
	// run, even if a caller bypasses LoadDeliveryRoute.
	result, err := r.DB.ExecContext(ctx, `INSERT INTO scheduled_task_deliveries (id,run_id,delivery_kind,attempt,stage,outcome,completed_at) SELECT ?,sr.id,'result_card',1,CASE WHEN sr.silent THEN NULL ELSE 'pending' END,CASE WHEN sr.silent THEN 'suppressed' ELSE NULL END,CASE WHEN sr.silent THEN ? ELSE NULL END FROM scheduled_task_runs sr WHERE sr.id=? AND sr.state IN ('succeeded','failed','cancelled','unknown','skipped')`, id, now, runID)
	if err == nil {
		count, countErr := result.RowsAffected()
		if countErr != nil {
			return DeliveryIntent{}, fmt.Errorf("read schedule result delivery creation: %w", countErr)
		}
		if count != 1 {
			return DeliveryIntent{}, ErrRunClaimLost
		}
		return r.loadDelivery(ctx, runID, DeliveryResultCard, 1)
	}
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
		return DeliveryIntent{}, fmt.Errorf("create schedule result delivery: %w", err)
	}
	return r.loadDelivery(ctx, runID, DeliveryResultCard, 1)
}

func (r Repository) loadDelivery(ctx context.Context, runID string, kind DeliveryKind, attempt int) (DeliveryIntent, error) {
	var row DeliveryIntent
	var stage, outcome sql.NullString
	err := r.DB.QueryRowContext(ctx, `SELECT id,run_id,delivery_kind,attempt,stage,outcome FROM scheduled_task_deliveries WHERE run_id=? AND delivery_kind=? AND attempt=?`, runID, string(kind), attempt).Scan(&row.ID, &row.RunID, &row.Kind, &row.Attempt, &stage, &outcome)
	if err != nil {
		return DeliveryIntent{}, fmt.Errorf("load schedule delivery: %w", err)
	}
	row.Stage, row.Outcome = DeliveryStage(stage.String), DeliveryOutcome(outcome.String)
	return row, nil
}

// ClaimDelivery is the final durable operation before an external Feishu API
// call. A false result means another process already owns or finalized it.
func (r Repository) ClaimDelivery(ctx context.Context, deliveryID string) (bool, error) {
	if r.DB == nil || strings.TrimSpace(deliveryID) == "" {
		return false, fmt.Errorf("schedule delivery identity is invalid")
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE scheduled_task_deliveries SET stage='in_flight',updated_at=CURRENT_TIMESTAMP(3) WHERE id=? AND stage='pending' AND outcome IS NULL`, deliveryID)
	if err != nil {
		return false, fmt.Errorf("claim schedule delivery: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read schedule delivery claim: %w", err)
	}
	return count == 1, nil
}

// CompleteDelivery records the known external outcome after an in-flight API
// call. Unknown is terminal too: it is intentionally never converted back to
// pending because visibility to the user cannot be determined safely.
func (r Repository) CompleteDelivery(ctx context.Context, deliveryID string, outcome DeliveryOutcome, messageIDHMAC string) (bool, error) {
	if r.DB == nil || strings.TrimSpace(deliveryID) == "" {
		return false, fmt.Errorf("schedule delivery identity is invalid")
	}
	if outcome != DeliverySent && outcome != DeliveryRejected && outcome != DeliveryUnknown {
		return false, fmt.Errorf("schedule delivery outcome is invalid")
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	var message any
	if messageIDHMAC != "" {
		message = messageIDHMAC
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE scheduled_task_deliveries SET stage=NULL,outcome=?,feishu_bot_message_id_hmac=?,completed_at=? WHERE id=? AND stage='in_flight' AND outcome IS NULL`, string(outcome), message, now, deliveryID)
	if err != nil {
		return false, fmt.Errorf("complete schedule delivery: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read schedule delivery completion: %w", err)
	}
	return count == 1, nil
}

// CreateFallbackAfterRejected creates the one text fallback only when the
// primary result card has a known, explicit rejected result. It never follows
// unknown or suppressed outcomes.
func (r Repository) CreateFallbackAfterRejected(ctx context.Context, runID string) (DeliveryIntent, error) {
	if r.DB == nil || strings.TrimSpace(runID) == "" {
		return DeliveryIntent{}, fmt.Errorf("schedule fallback delivery identity is invalid")
	}
	newID := r.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	id := newID()
	if strings.TrimSpace(id) == "" {
		return DeliveryIntent{}, fmt.Errorf("schedule fallback delivery id is required")
	}
	result, err := r.DB.ExecContext(ctx, `INSERT INTO scheduled_task_deliveries (id,run_id,delivery_kind,attempt,stage) SELECT ?,run_id,'fallback_text',1,'pending' FROM scheduled_task_deliveries WHERE run_id=? AND delivery_kind='result_card' AND attempt=1 AND stage IS NULL AND outcome='rejected'`, id, runID)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
			return DeliveryIntent{}, fmt.Errorf("create schedule fallback delivery: %w", err)
		}
		return r.loadDelivery(ctx, runID, DeliveryFallbackText, 1)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return DeliveryIntent{}, fmt.Errorf("read schedule fallback creation: %w", err)
	}
	if count != 1 {
		return DeliveryIntent{}, nil
	}
	return DeliveryIntent{ID: id, RunID: runID, Kind: DeliveryFallbackText, Attempt: 1, Stage: DeliveryPending}, nil
}

// ReconcileInFlightDeliveries runs at startup. An interrupted request may
// already be visible to users, so it becomes unknown and is never resent.
func (r Repository) ReconcileInFlightDeliveries(ctx context.Context, now time.Time) (int64, error) {
	if r.DB == nil || now.IsZero() {
		return 0, fmt.Errorf("schedule delivery reconciliation is invalid")
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE scheduled_task_deliveries SET stage=NULL,outcome='unknown',completed_at=? WHERE stage='in_flight' AND outcome IS NULL`, now.UTC())
	if err != nil {
		return 0, fmt.Errorf("reconcile in-flight schedule deliveries: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read schedule delivery reconciliation: %w", err)
	}
	return count, nil
}

// LoadDeliveryRoute reconstructs a terminal run's original owner destination
// just long enough to send its automatic result. The owner open ID remains
// memory-only and is selected from the task record, never from a run payload
// or model output.
func (r Repository) LoadDeliveryRoute(ctx context.Context, runID string) (DeliveryRoute, error) {
	if r.DB == nil || strings.TrimSpace(runID) == "" {
		return DeliveryRoute{}, fmt.Errorf("schedule delivery route identity is invalid")
	}
	var row struct {
		appID, chatGroupID, kind, ownerOpenID string
		chatType, chatID                      string
		silent                                bool
	}
	err := r.DB.QueryRowContext(ctx, `SELECT t.app_id,t.chat_group_id,t.kind,t.creator_open_id,cg.chat_type,cg.chat_id,sr.silent FROM scheduled_task_runs sr JOIN scheduled_tasks t ON t.id=sr.task_id JOIN apps a ON a.id=t.app_id JOIN chat_groups cg ON cg.id=t.chat_group_id WHERE sr.id=? AND sr.state IN ('succeeded','failed','cancelled','unknown','skipped') AND a.enabled=TRUE AND cg.schedule_enabled=TRUE`, runID).Scan(&row.appID, &row.chatGroupID, &row.kind, &row.ownerOpenID, &row.chatType, &row.chatID, &row.silent)
	if err == sql.ErrNoRows {
		return DeliveryRoute{}, ErrRunClaimLost
	}
	if err != nil {
		return DeliveryRoute{}, fmt.Errorf("load schedule delivery route: %w", err)
	}
	if row.kind != string(TaskPrompt) && row.kind != string(TaskScript) {
		return DeliveryRoute{}, fmt.Errorf("stored schedule delivery kind is invalid")
	}
	if row.ownerOpenID == "" {
		return DeliveryRoute{}, fmt.Errorf("schedule delivery owner is missing")
	}
	route := DeliveryRoute{RunID: runID, AppID: row.appID, Kind: TaskKind(row.kind), Silent: row.silent}
	if row.chatType == "group" {
		route.ReplyID, route.ReplyType = row.chatID, "chat_id"
	} else {
		route.ReplyID, route.ReplyType = row.ownerOpenID, "open_id"
	}
	if route.ReplyID == "" || route.ReplyType == "" {
		return DeliveryRoute{}, fmt.Errorf("schedule delivery reply route is invalid")
	}
	return route, nil
}

// MessageIDHMAC stores only a domain-separated keyed digest of an external
// message identifier; raw Feishu IDs never enter the S06 database or logs.
func (r Repository) MessageIDHMAC(messageID string) (string, error) {
	if strings.TrimSpace(messageID) == "" {
		return "", fmt.Errorf("schedule delivery message id is required")
	}
	digest, _, err := r.Protector.Owners.HMAC([]byte("scheduled_delivery_message_id\x00" + messageID))
	if err != nil {
		return "", fmt.Errorf("digest schedule delivery message id: %w", err)
	}
	return digest, nil
}
