package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/kid0317/codex-workspace-bot/internal/catalog"
)

// CatalogUpgradeState is persisted before each externally visible catalog
// transition. A worker can therefore resume safely after a crash without
// accidentally resuming a thread with an obsolete dynamic-tool catalog.
type CatalogUpgradeState = catalog.UpgradeState
type CatalogUpgrade = catalog.Upgrade

const (
	CatalogUpgradeStable         = catalog.Stable
	CatalogUpgradeArchivePending = catalog.ArchivePending
	CatalogUpgradeStartPending   = catalog.StartPending
)

// PrepareCatalogUpgrade records archive_pending before the caller contacts
// App Server. If an unfinished upgrade already exists it is returned exactly
// as stored so recovery repeats only the safe next action.
func (s *Store) PrepareCatalogUpgrade(ctx context.Context, chatGroupID, target string) (CatalogUpgrade, error) {
	if strings.TrimSpace(chatGroupID) == "" || strings.TrimSpace(target) == "" {
		return CatalogUpgrade{}, fmt.Errorf("catalog upgrade group and target are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return CatalogUpgrade{}, fmt.Errorf("begin catalog upgrade: %w", err)
	}
	defer tx.Rollback()
	state, err := scanCatalogUpgrade(tx.QueryRowContext(ctx, `SELECT codex_thread_id,codex_tool_catalog_version,catalog_upgrade_state,catalog_upgrade_from_thread_id,catalog_upgrade_target FROM chat_groups WHERE id=? FOR UPDATE`, chatGroupID))
	if err != nil {
		return CatalogUpgrade{}, err
	}
	if state.State != CatalogUpgradeStable || state.CurrentVersion == target {
		if err := tx.Commit(); err != nil {
			return CatalogUpgrade{}, fmt.Errorf("commit catalog upgrade read: %w", err)
		}
		return state, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chat_groups SET catalog_upgrade_state='archive_pending',catalog_upgrade_from_thread_id=?,catalog_upgrade_target=?,updated_at=CURRENT_TIMESTAMP(3) WHERE id=?`, nullableThread(state.CurrentThreadID), target, chatGroupID); err != nil {
		return CatalogUpgrade{}, fmt.Errorf("persist catalog archive pending: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CatalogUpgrade{}, fmt.Errorf("commit catalog archive pending: %w", err)
	}
	state.State = CatalogUpgradeArchivePending
	state.FromThreadID = state.CurrentThreadID
	state.Target = target
	return state, nil
}

func scanCatalogUpgrade(row *sql.Row) (CatalogUpgrade, error) {
	var thread, version, from, target sql.NullString
	var state string
	if err := row.Scan(&thread, &version, &state, &from, &target); err != nil {
		return CatalogUpgrade{}, fmt.Errorf("load catalog upgrade: %w", err)
	}
	result := CatalogUpgrade{CurrentThreadID: thread.String, CurrentVersion: version.String, State: catalog.UpgradeState(state), FromThreadID: from.String, Target: target.String}
	if result.State != CatalogUpgradeStable && result.State != CatalogUpgradeArchivePending && result.State != CatalogUpgradeStartPending {
		return CatalogUpgrade{}, fmt.Errorf("stored catalog upgrade state is invalid")
	}
	return result, nil
}

// AdvanceCatalogUpgradeAfterArchive clears the old thread only after App
// Server accepted its archive request. The comparison against the recorded
// source thread prevents a stale worker from clearing a newer session.
func (s *Store) AdvanceCatalogUpgradeAfterArchive(ctx context.Context, chatGroupID, archivedThreadID string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, `UPDATE chat_groups SET codex_thread_id=NULL,catalog_upgrade_state='start_pending',updated_at=CURRENT_TIMESTAMP(3) WHERE id=? AND catalog_upgrade_state='archive_pending' AND catalog_upgrade_from_thread_id <=> ?`, chatGroupID, nullableThread(archivedThreadID))
	if err != nil {
		return false, fmt.Errorf("advance catalog upgrade after archive: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read catalog archive transition: %w", err)
	}
	return changed == 1, nil
}

// CompleteCatalogUpgrade publishes the new thread and catalog together. A
// restarted process observing start_pending must call this conditional update
// rather than ever attempting thread/resume for the archived catalog.
func (s *Store) CompleteCatalogUpgrade(ctx context.Context, chatGroupID, target, threadID string) (bool, error) {
	if strings.TrimSpace(chatGroupID) == "" || strings.TrimSpace(target) == "" || strings.TrimSpace(threadID) == "" {
		return false, fmt.Errorf("catalog completion fields are required")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE chat_groups SET codex_thread_id=?,codex_toolset_version=?,codex_tool_catalog_version=?,catalog_upgrade_state='stable',catalog_upgrade_from_thread_id=NULL,catalog_upgrade_target=NULL,updated_at=CURRENT_TIMESTAMP(3) WHERE id=? AND catalog_upgrade_state='start_pending' AND catalog_upgrade_target=?`, threadID, target, target, chatGroupID, target)
	if err != nil {
		return false, fmt.Errorf("complete catalog upgrade: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read catalog completion: %w", err)
	}
	return changed == 1, nil
}
