package task_test

import (
	"context"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/model"
	"github.com/kid0317/codex-workspace-bot/internal/task"
)

func TestSchedulerPreventsOverlappingRuns(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	s := task.NewScheduler(func(ctx context.Context, t model.Task) error {
		started <- struct{}{}
		<-release
		return nil
	})
	job := model.Task{ID: "demo/job", AppID: "demo", Enabled: true, CronExpr: "* * * * *", SendOutput: false}
	if err := s.Add(job); err != nil {
		t.Fatal(err)
	}
	s.Trigger(context.Background(), "demo/job")
	s.Trigger(context.Background(), "demo/job")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first run did not start")
	}
	select {
	case <-started:
		t.Fatal("overlapping run started")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
}
