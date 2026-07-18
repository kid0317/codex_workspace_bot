package schedule

import (
	"context"
	"fmt"
	"time"
)

// DueTask contains only the immutable observation necessary to claim a due
// slot. The scheduler must never dispatch directly from this value: ClaimDue
// re-locks it and produces the durable run snapshot.
type DueTask struct {
	ID           string
	Version      uint64
	NextRunAt    time.Time
	Kind         TaskKind
	RouteRevoked bool
}

type DueStore interface {
	ListDue(context.Context, time.Time, int) ([]DueTask, error)
	ClaimDue(context.Context, DueClaim) (ClaimedRun, error)
	SkipMisfire(context.Context, DueTask, time.Time) error
	FailRouteRevoked(context.Context, DueTask, time.Time) error
}

type RunDispatcher interface {
	Dispatch(context.Context, ClaimedRun) error
}

// Scheduler owns no unbounded backlog. A run is either durably claimed then
// synchronously handed to its bounded dispatcher, or the dispatcher returns a
// deterministic terminal error in a later integration layer.
type Scheduler struct {
	Store             DueStore
	Dispatch          RunDispatcher
	Now               func() time.Time
	ScanLimit         int
	MisfireGrace      time.Duration
	ClaimLease        time.Duration
	MaxPromptDispatch int
	TickInterval      time.Duration
}

// Run performs a startup scan then periodic bounded scans until shutdown.
// It does not own goroutines after ctx is cancelled and therefore cannot
// claim new work during service shutdown.
func (s Scheduler) Run(ctx context.Context) error {
	if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	interval := s.TickInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
				return err
			}
		}
	}
}

// ListDue is the bounded authoritative scan. Disabled App/chat routes remain
// visible long enough to record one failed_route_revoked terminal slot;
// ClaimDue still repeats the route checks under lock before producing a run.
func (r Repository) ListDue(ctx context.Context, now time.Time, limit int) ([]DueTask, error) {
	if r.DB == nil {
		return nil, fmt.Errorf("schedule store database is required")
	}
	if now.IsZero() || limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("schedule due scan is invalid")
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT t.id,t.version,t.next_run_at,t.kind,a.enabled,cg.schedule_enabled FROM scheduled_tasks t JOIN apps a ON a.id=t.app_id JOIN chat_groups cg ON cg.id=t.chat_group_id WHERE t.enabled=TRUE AND t.next_run_at IS NOT NULL AND t.next_run_at <= ? ORDER BY t.next_run_at ASC,t.id ASC LIMIT ?`, now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list due schedule tasks: %w", err)
	}
	defer rows.Close()
	var due []DueTask
	for rows.Next() {
		var task DueTask
		var kind string
		var appEnabled, scheduleEnabled bool
		if err := rows.Scan(&task.ID, &task.Version, &task.NextRunAt, &kind, &appEnabled, &scheduleEnabled); err != nil {
			return nil, fmt.Errorf("scan due schedule task: %w", err)
		}
		if kind != string(TaskPrompt) && kind != string(TaskScript) {
			return nil, fmt.Errorf("stored due schedule task kind is invalid")
		}
		task.Kind = TaskKind(kind)
		task.NextRunAt = task.NextRunAt.UTC()
		task.RouteRevoked = !appEnabled || !scheduleEnabled
		due = append(due, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due schedule tasks: %w", err)
	}
	return due, nil
}

func (s Scheduler) Tick(ctx context.Context) error {
	if s.Store == nil || s.Dispatch == nil {
		return fmt.Errorf("schedule store and dispatcher are required")
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	limit := s.ScanLimit
	if limit <= 0 {
		limit = 100
	}
	grace := s.MisfireGrace
	if grace <= 0 {
		grace = time.Minute
	}
	lease := s.ClaimLease
	if lease <= 0 {
		lease = 30 * time.Second
	}
	maxPrompts := s.MaxPromptDispatch
	if maxPrompts <= 0 {
		maxPrompts = 20
	}
	due, err := s.Store.ListDue(ctx, now, limit)
	if err != nil {
		return fmt.Errorf("list due schedule tasks: %w", err)
	}
	promptCount := 0
	for _, task := range due {
		if task.ID == "" || task.Version == 0 || task.NextRunAt.IsZero() {
			continue
		}
		if task.RouteRevoked {
			if err := s.Store.FailRouteRevoked(ctx, task, now); err != nil && err != ErrDueClaimConflict {
				return fmt.Errorf("record schedule route revoke: %w", err)
			}
			continue
		}
		if task.NextRunAt.Before(now.Add(-grace)) {
			if err := s.Store.SkipMisfire(ctx, task, now); err != nil {
				return fmt.Errorf("skip schedule misfire: %w", err)
			}
			continue
		}
		if task.Kind == TaskPrompt && promptCount >= maxPrompts {
			continue
		}
		run, err := s.Store.ClaimDue(ctx, DueClaim{TaskID: task.ID, ObservedVersion: task.Version, ObservedSlot: task.NextRunAt, Lease: lease})
		if err == ErrDueClaimConflict {
			continue
		}
		if err != nil {
			return fmt.Errorf("claim due schedule task: %w", err)
		}
		if task.Kind == TaskPrompt {
			promptCount++
		}
		if err := s.Dispatch.Dispatch(ctx, run); err != nil {
			return fmt.Errorf("dispatch claimed schedule run: %w", err)
		}
	}
	return nil
}
