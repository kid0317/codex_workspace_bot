package task

import (
	"context"
	"fmt"
	"sync"

	"github.com/kid0317/codex-workspace-bot/internal/model"
)

type Scheduler struct {
	mu      sync.Mutex
	tasks   map[string]model.Task
	running map[string]bool
	run     func(context.Context, model.Task) error
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
	s.tasks[t.ID] = t
	return nil
}

func (s *Scheduler) Trigger(ctx context.Context, id string) {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok || s.running[id] {
		s.mu.Unlock()
		return
	}
	s.running[id] = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			s.running[id] = false
			s.mu.Unlock()
		}()
		_ = s.run(ctx, t)
	}()
}
