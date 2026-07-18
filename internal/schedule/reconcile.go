package schedule

import (
	"context"
	"fmt"
	"time"
)

// ReconcileInterruptedRuns marks claimed or executing runs whose dispatch may
// have begun before an interruption.
// It deliberately does not dispatch, send delivery, or alter future task
// definitions; an unknown execution is never safe to replay automatically.
func (r Repository) ReconcileInterruptedRuns(ctx context.Context, now time.Time) (int64, error) {
	if r.DB == nil {
		return 0, fmt.Errorf("schedule store database is required")
	}
	if now.IsZero() {
		return 0, fmt.Errorf("schedule reconcile time is required")
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE scheduled_task_runs SET state='unknown',error_code='unknown_interrupted',lease_until=NULL,completed_at=? WHERE state IN ('claimed','queued','running')`, now.UTC())
	if err != nil {
		return 0, fmt.Errorf("reconcile interrupted schedule runs: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read interrupted schedule reconciliation result: %w", err)
	}
	return count, nil
}
