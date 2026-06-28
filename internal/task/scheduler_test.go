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

func TestSchedulerAddReplaceRemoveAndList(t *testing.T) {
	s := task.NewScheduler(func(ctx context.Context, t model.Task) error { return nil })
	first := model.Task{ID: "demo/job", AppID: "demo", Enabled: true, CronExpr: "0 * * * *", Prompt: "first"}
	replacement := model.Task{ID: "demo/job", AppID: "demo", Enabled: true, CronExpr: "5 * * * *", Prompt: "second"}
	if err := s.Add(first); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(replacement); err != nil {
		t.Fatal(err)
	}
	tasks := s.Tasks()
	if len(tasks) != 1 || tasks[0].Prompt != "second" {
		t.Fatalf("scheduler tasks after replace = %#v", tasks)
	}
	s.Remove("demo/job")
	if len(s.Tasks()) != 0 {
		t.Fatalf("scheduler tasks after remove = %#v", s.Tasks())
	}
}

func TestSchedulerClosePreventsNewTriggers(t *testing.T) {
	started := make(chan struct{}, 1)
	s := task.NewScheduler(func(ctx context.Context, t model.Task) error {
		started <- struct{}{}
		return nil
	})
	if err := s.Add(model.Task{ID: "demo/job", AppID: "demo", Enabled: true, CronExpr: "* * * * *"}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s.Trigger(context.Background(), "demo/job")
	select {
	case <-started:
		t.Fatal("closed scheduler started a task")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSchedulerStartRunsEnabledCronTaskAndCloseDrains(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	s := task.NewScheduler(func(ctx context.Context, t model.Task) error {
		started <- struct{}{}
		<-release
		return nil
	})
	if err := s.Add(model.Task{ID: "demo/job", AppID: "demo", Enabled: true, CronExpr: "* * * * *"}); err != nil {
		t.Fatal(err)
	}
	s.Start(context.Background(), 10*time.Millisecond)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not automatically run enabled cron task")
	}
	done := make(chan struct{})
	go func() {
		s.Close()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("scheduler close returned before running task drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler close did not drain running task")
	}
}

func TestSchedulerDoesNotRunCronEveryTickWithinSameMinute(t *testing.T) {
	started := make(chan struct{}, 2)
	s := task.NewScheduler(func(ctx context.Context, t model.Task) error {
		started <- struct{}{}
		return nil
	})
	now := time.Now().Truncate(time.Minute)
	job := model.Task{ID: "demo/job", AppID: "demo", Enabled: true, CronExpr: "* * * * *"}
	if err := s.Add(job); err != nil {
		t.Fatal(err)
	}
	s.RunDue(context.Background(), now)
	s.RunDue(context.Background(), now.Add(10*time.Second))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first due run did not start")
	}
	select {
	case <-started:
		t.Fatal("task ran twice in the same cron minute")
	case <-time.After(50 * time.Millisecond):
	}
	s.RunDue(context.Background(), now.Add(time.Minute))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("task did not run in next cron minute")
	}
}

func TestSchedulerHonorsSpecificCronMinuteAndHour(t *testing.T) {
	started := make(chan struct{}, 1)
	s := task.NewScheduler(func(ctx context.Context, t model.Task) error {
		started <- struct{}{}
		return nil
	})
	job := model.Task{ID: "demo/daily", AppID: "demo", Enabled: true, CronExpr: "30 9 * * *"}
	if err := s.Add(job); err != nil {
		t.Fatal(err)
	}
	s.RunDue(context.Background(), time.Date(2026, 6, 28, 9, 29, 0, 0, time.Local))
	select {
	case <-started:
		t.Fatal("task ran before matching cron minute")
	case <-time.After(20 * time.Millisecond):
	}
	s.RunDue(context.Background(), time.Date(2026, 6, 28, 9, 30, 0, 0, time.Local))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("task did not run at matching cron minute")
	}
}
