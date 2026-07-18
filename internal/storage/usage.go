package storage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/kid0317/codex-workspace-bot/internal/observability"
)

// RecordTurnUsage stores one authoritative completed Turn. The per-thread
// table is the single source for session totals: an authoritative increment is
// added to its Thread's effective cumulative value, and a later App Server
// cumulative snapshot can replace it only when it is at least as new in every
// counter. This prevents a fallback snapshot from losing usage from another
// Thread after /new.
func (s *Store) RecordTurnUsage(ctx context.Context, record observability.TurnUsageRecord) (observability.SessionUsageTotal, bool, error) {
	if record.TraceID == "" || record.TurnID == "" {
		return observability.SessionUsageTotal{}, false, fmt.Errorf("record turn usage: trace id and turn id are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return observability.SessionUsageTotal{}, false, fmt.Errorf("begin turn usage: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT IGNORE INTO turn_usage_ledger (trace_id,thread_id,turn_id,input_tokens,output_tokens,cached_input_tokens,reasoning_output_tokens,total_tokens) VALUES (?,?,?,?,?,?,?,?)`,
		record.TraceID, nullableString(record.ThreadID), record.TurnID,
		record.Usage.InputTokens, record.Usage.OutputTokens, record.Usage.CachedInputTokens, record.Usage.ReasoningOutputTokens, record.Usage.TotalTokens)
	if err != nil {
		return observability.SessionUsageTotal{}, false, fmt.Errorf("insert turn usage ledger: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return observability.SessionUsageTotal{}, false, fmt.Errorf("turn usage ledger rows: %w", err)
	}
	inserted := rows == 1
	var total observability.SessionUsageTotal
	if record.Session.Valid() {
		if inserted {
			if err := addAuthoritativeThreadUsage(ctx, tx, record.Session, record.TraceID, record.ThreadID, record.TurnID, record.Usage); err != nil {
				return observability.SessionUsageTotal{}, false, err
			}
		}
		total, err = replaceSessionUsageFromThreads(ctx, tx, record.Session, record.TraceID, record.ThreadID, record.TurnID, inserted)
		if err != nil {
			return observability.SessionUsageTotal{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return observability.SessionUsageTotal{}, false, fmt.Errorf("commit turn usage: %w", err)
	}
	return total, inserted, nil
}

// RecordThreadUsageSnapshot records an App Server cumulative Thread total
// only if it is a high-water mark for every counter. Older or partial
// snapshots are intentionally ignored, so delayed notifications cannot make
// plaintext session totals go backwards.
func (s *Store) RecordThreadUsageSnapshot(ctx context.Context, record observability.ThreadUsageSnapshotRecord) (observability.SessionUsageTotal, error) {
	if record.ThreadID == "" || !record.Session.Valid() {
		return observability.SessionUsageTotal{}, fmt.Errorf("record thread usage snapshot: thread and session are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return observability.SessionUsageTotal{}, fmt.Errorf("begin thread usage snapshot: %w", err)
	}
	defer tx.Rollback()
	current, found, err := readThreadUsageForUpdate(ctx, tx, record.Session, record.ThreadID)
	if err != nil {
		return observability.SessionUsageTotal{}, err
	}
	if !found {
		_, err = tx.ExecContext(ctx, `INSERT INTO thread_usage_snapshots (app_id,chat_type,chat_id,thread_id,last_trace_id,last_turn_id,input_tokens,output_tokens,cached_input_tokens,reasoning_output_tokens,total_tokens) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, record.Session.AppID, record.Session.ChatType, record.Session.ChatID, record.ThreadID, nullableString(record.TraceID), nullableString(record.TurnID), record.Usage.InputTokens, record.Usage.OutputTokens, record.Usage.CachedInputTokens, record.Usage.ReasoningOutputTokens, record.Usage.TotalTokens)
		if err != nil {
			return observability.SessionUsageTotal{}, fmt.Errorf("insert thread usage snapshot: %w", err)
		}
	} else if snapshotAtLeast(record.Usage, current) {
		_, err = tx.ExecContext(ctx, `UPDATE thread_usage_snapshots SET last_trace_id=?,last_turn_id=?,input_tokens=?,output_tokens=?,cached_input_tokens=?,reasoning_output_tokens=?,total_tokens=?,updated_at=CURRENT_TIMESTAMP(3) WHERE app_id=? AND chat_type=? AND chat_id=? AND thread_id=?`, nullableString(record.TraceID), nullableString(record.TurnID), record.Usage.InputTokens, record.Usage.OutputTokens, record.Usage.CachedInputTokens, record.Usage.ReasoningOutputTokens, record.Usage.TotalTokens, record.Session.AppID, record.Session.ChatType, record.Session.ChatID, record.ThreadID)
		if err != nil {
			return observability.SessionUsageTotal{}, fmt.Errorf("update thread usage snapshot: %w", err)
		}
	}
	total, err := replaceSessionUsageFromThreads(ctx, tx, record.Session, record.TraceID, record.ThreadID, record.TurnID, false)
	if err != nil {
		return observability.SessionUsageTotal{}, err
	}
	if err := tx.Commit(); err != nil {
		return observability.SessionUsageTotal{}, fmt.Errorf("commit thread usage snapshot: %w", err)
	}
	return total, nil
}

func addAuthoritativeThreadUsage(ctx context.Context, tx *sql.Tx, session observability.SessionUsageKey, traceID, threadID, turnID string, usage observability.Usage) error {
	if threadID == "" {
		return fmt.Errorf("add authoritative thread usage: thread id is required")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO thread_usage_snapshots (app_id,chat_type,chat_id,thread_id,last_trace_id,last_turn_id,input_tokens,output_tokens,cached_input_tokens,reasoning_output_tokens,total_tokens) VALUES (?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE last_trace_id=VALUES(last_trace_id),last_turn_id=VALUES(last_turn_id),input_tokens=input_tokens+VALUES(input_tokens),output_tokens=output_tokens+VALUES(output_tokens),cached_input_tokens=cached_input_tokens+VALUES(cached_input_tokens),reasoning_output_tokens=reasoning_output_tokens+VALUES(reasoning_output_tokens),total_tokens=total_tokens+VALUES(total_tokens),updated_at=CURRENT_TIMESTAMP(3)`, session.AppID, session.ChatType, session.ChatID, threadID, nullableString(traceID), nullableString(turnID), usage.InputTokens, usage.OutputTokens, usage.CachedInputTokens, usage.ReasoningOutputTokens, usage.TotalTokens)
	if err != nil {
		return fmt.Errorf("add authoritative thread usage: %w", err)
	}
	return nil
}

func readThreadUsageForUpdate(ctx context.Context, tx *sql.Tx, session observability.SessionUsageKey, threadID string) (observability.Usage, bool, error) {
	var usage observability.Usage
	err := tx.QueryRowContext(ctx, `SELECT input_tokens,output_tokens,cached_input_tokens,reasoning_output_tokens,total_tokens FROM thread_usage_snapshots WHERE app_id=? AND chat_type=? AND chat_id=? AND thread_id=? FOR UPDATE`, session.AppID, session.ChatType, session.ChatID, threadID).Scan(&usage.InputTokens, &usage.OutputTokens, &usage.CachedInputTokens, &usage.ReasoningOutputTokens, &usage.TotalTokens)
	if err == sql.ErrNoRows {
		return observability.Usage{}, false, nil
	}
	if err != nil {
		return observability.Usage{}, false, fmt.Errorf("read thread usage snapshot: %w", err)
	}
	return usage, true, nil
}

func snapshotAtLeast(candidate, current observability.Usage) bool {
	return candidate.InputTokens >= current.InputTokens &&
		candidate.OutputTokens >= current.OutputTokens &&
		candidate.CachedInputTokens >= current.CachedInputTokens &&
		candidate.ReasoningOutputTokens >= current.ReasoningOutputTokens &&
		candidate.TotalTokens >= current.TotalTokens
}

func replaceSessionUsageFromThreads(ctx context.Context, tx *sql.Tx, session observability.SessionUsageKey, traceID, threadID, turnID string, incrementCompleted bool) (observability.SessionUsageTotal, error) {
	// Materialize and lock the conversation row before summing. That serializes
	// concurrent Threads in one chat and keeps the completed-turn count stable.
	_, err := tx.ExecContext(ctx, `INSERT IGNORE INTO session_usage_totals (app_id,chat_type,chat_id,input_tokens,output_tokens,cached_input_tokens,reasoning_output_tokens,total_tokens,completed_turn_count,last_trace_id,last_thread_id,last_turn_id) VALUES (?,?,?,?,?,?,?,?,0,?,?,?)`, session.AppID, session.ChatType, session.ChatID, int64(0), int64(0), int64(0), int64(0), int64(0), nil, nil, nil)
	if err != nil {
		return observability.SessionUsageTotal{}, fmt.Errorf("initialize session usage total: %w", err)
	}
	var completed int64
	if err := tx.QueryRowContext(ctx, `SELECT completed_turn_count FROM session_usage_totals WHERE app_id=? AND chat_type=? AND chat_id=? FOR UPDATE`, session.AppID, session.ChatType, session.ChatID).Scan(&completed); err != nil {
		return observability.SessionUsageTotal{}, fmt.Errorf("lock session usage total: %w", err)
	}
	if incrementCompleted {
		completed++
	}
	var usage observability.Usage
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(cached_input_tokens),0),COALESCE(SUM(reasoning_output_tokens),0),COALESCE(SUM(total_tokens),0) FROM thread_usage_snapshots WHERE app_id=? AND chat_type=? AND chat_id=?`, session.AppID, session.ChatType, session.ChatID).Scan(&usage.InputTokens, &usage.OutputTokens, &usage.CachedInputTokens, &usage.ReasoningOutputTokens, &usage.TotalTokens)
	if err != nil {
		return observability.SessionUsageTotal{}, fmt.Errorf("sum thread usage snapshots: %w", err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE session_usage_totals SET input_tokens=?,output_tokens=?,cached_input_tokens=?,reasoning_output_tokens=?,total_tokens=?,completed_turn_count=?,last_trace_id=?,last_thread_id=?,last_turn_id=?,updated_at=CURRENT_TIMESTAMP(3) WHERE app_id=? AND chat_type=? AND chat_id=?`, usage.InputTokens, usage.OutputTokens, usage.CachedInputTokens, usage.ReasoningOutputTokens, usage.TotalTokens, completed, nullableString(traceID), nullableString(threadID), nullableString(turnID), session.AppID, session.ChatType, session.ChatID)
	if err != nil {
		return observability.SessionUsageTotal{}, fmt.Errorf("replace session usage from threads: %w", err)
	}
	return observability.SessionUsageTotal{Usage: usage, CompletedTurnCount: completed}, nil
}
