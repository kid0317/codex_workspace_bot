package task

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/model"
)

type Scheduler struct {
	mu      sync.Mutex
	tasks   map[string]model.Task
	running map[string]bool
	run     func(context.Context, model.Task) error
	closed  bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func NewScheduler(run func(context.Context, model.Task) error) *Scheduler {
	return &Scheduler{tasks: map[string]model.Task{}, running: map[string]bool{}, run: run}
}

func (s *Scheduler) Add(t model.Task) error {
	if t.ID == "" {
		return fmt.Errorf("任务 ID 为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("scheduler closed")
	}
	s.tasks[t.ID] = t
	return nil
}

func (s *Scheduler) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tasks, id)
	delete(s.running, id)
}

func (s *Scheduler) Tasks() []model.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, t)
	}
	return out
}

func (s *Scheduler) Close() {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Scheduler) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	s.mu.Lock()
	if s.closed || s.cancel != nil {
		s.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				s.RunDue(runCtx, time.Now())
			}
		}
	}()
}

func (s *Scheduler) RunDue(ctx context.Context, now time.Time) {
	for _, t := range s.Tasks() {
		if t.Enabled && shouldRunAt(t, now) {
			s.TriggerAt(ctx, t.ID, now)
		}
	}
}

func (s *Scheduler) Trigger(ctx context.Context, id string) {
	s.TriggerAt(ctx, id, time.Now())
}

func (s *Scheduler) TriggerAt(ctx context.Context, id string, now time.Time) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	t, ok := s.tasks[id]
	if !ok || s.running[id] {
		s.mu.Unlock()
		return
	}
	if !now.IsZero() {
		minute := now.Truncate(time.Minute)
		t.LastRunAt = &minute
		s.tasks[id] = t
	}
	s.running[id] = true
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			s.running[id] = false
			s.mu.Unlock()
			s.wg.Done()
		}()
		_ = s.run(ctx, t)
	}()
}

func shouldRunAt(t model.Task, now time.Time) bool {
	if strings.TrimSpace(t.CronExpr) == "" {
		return false
	}
	if !cronMatches(t.CronExpr, now) {
		return false
	}
	if t.LastRunAt == nil {
		return true
	}
	return t.LastRunAt.Before(now.Truncate(time.Minute))
}

func cronMatches(expr string, now time.Time) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	checks := []struct {
		field string
		value int
	}{
		{fields[0], now.Minute()},
		{fields[1], now.Hour()},
		{fields[2], now.Day()},
		{fields[3], int(now.Month())},
		{fields[4], int(now.Weekday())},
	}
	for _, check := range checks {
		if !cronFieldMatches(check.field, check.value) {
			return false
		}
	}
	return true
}

func cronFieldMatches(field string, value int) bool {
	if field == "*" {
		return true
	}
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "*" {
			return true
		}
		if strings.HasPrefix(part, "*/") {
			step, err := strconv.Atoi(strings.TrimPrefix(part, "*/"))
			if err != nil || step <= 0 {
				return false
			}
			if value%step == 0 {
				return true
			}
			continue
		}
		n, err := strconv.Atoi(part)
		if err == nil && n == value {
			return true
		}
	}
	return false
}
