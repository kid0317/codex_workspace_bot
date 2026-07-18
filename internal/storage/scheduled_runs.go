package storage

import (
	"context"
	"fmt"
	"strings"
)

// MarkScheduledRunning and CompleteScheduledRun satisfy worker.ScheduledRunLifecycle
// without importing worker. All transitions are claim-token conditional.
func (s *Store) MarkScheduledRunning(ctx context.Context, runID, claimToken string) error {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(claimToken) == "" {
		return fmt.Errorf("scheduled run identity is invalid")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE scheduled_task_runs SET state='running',started_at=COALESCE(started_at,CURRENT_TIMESTAMP(3)),lease_until=DATE_ADD(CURRENT_TIMESTAMP(3),INTERVAL 330 SECOND) WHERE id=? AND claim_token=? AND state='queued'`, runID, claimToken)
	if err != nil {
		return fmt.Errorf("mark scheduled run running: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read scheduled running result: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("scheduled run claim is no longer active")
	}
	return nil
}

func (s *Store) CompleteScheduledRun(ctx context.Context, runID, claimToken string, succeeded bool, code string, durationMS int64) error {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(claimToken) == "" {
		return fmt.Errorf("scheduled run identity is invalid")
	}
	state := "failed"
	if succeeded {
		state = "succeeded"
		code = ""
	}
	var errorCode any
	if code != "" {
		errorCode = code
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE scheduled_task_runs SET state=?,error_code=?,lease_until=NULL,completed_at=CURRENT_TIMESTAMP(3),duration_ms=? WHERE id=? AND claim_token=? AND state IN ('queued','running')`, state, errorCode, durationMS, runID, claimToken)
	if err != nil {
		return fmt.Errorf("complete scheduled run: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read scheduled completion result: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("scheduled run claim is no longer active")
	}
	return nil
}
