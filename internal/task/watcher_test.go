package task_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/db"
	"github.com/kid0317/codex-workspace-bot/internal/model"
	"github.com/kid0317/codex-workspace-bot/internal/task"
)

func TestScanDirMirrorsYAMLTasksIntoStoreAndScheduler(t *testing.T) {
	root := t.TempDir()
	tasksDir := filepath.Join(root, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "daily.yaml"), []byte(`
name: Daily
cron: "0 9 * * *"
target_type: p2p
target_id: ou_user
send_output: true
prompt: hello
`), 0o644); err != nil {
		t.Fatal(err)
	}
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	scheduler := task.NewScheduler(func(ctx context.Context, t model.Task) error { return nil })

	if err := task.ScanDir(tasksDir, "demo", store, scheduler); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Tasks().All()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].ID != "demo/daily" {
		t.Fatalf("stored tasks = %#v", stored)
	}
	scheduled := scheduler.Tasks()
	if len(scheduled) != 1 || scheduled[0].ID != "demo/daily" {
		t.Fatalf("scheduled tasks = %#v", scheduled)
	}

	if err := os.WriteFile(filepath.Join(tasksDir, "daily.yaml"), []byte(`
name: Daily
enabled: false
send_output: false
prompt: hello
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := task.ScanDir(tasksDir, "demo", store, scheduler); err != nil {
		t.Fatal(err)
	}
	if len(scheduler.Tasks()) != 0 {
		t.Fatalf("disabled task remained scheduled: %#v", scheduler.Tasks())
	}
}

func TestScanDirRemovesDeletedYAMLFromScheduler(t *testing.T) {
	root := t.TempDir()
	tasksDir := filepath.Join(root, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(tasksDir, "daily.yaml")
	if err := os.WriteFile(taskPath, []byte(`
name: Daily
cron: "0 9 * * *"
target_type: p2p
target_id: ou_user
send_output: true
prompt: hello
`), 0o644); err != nil {
		t.Fatal(err)
	}
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	scheduler := task.NewScheduler(func(ctx context.Context, t model.Task) error { return nil })
	if err := task.ScanDir(tasksDir, "demo", store, scheduler); err != nil {
		t.Fatal(err)
	}
	if len(scheduler.Tasks()) != 1 {
		t.Fatalf("initial scheduled tasks = %#v", scheduler.Tasks())
	}
	if err := os.Remove(taskPath); err != nil {
		t.Fatal(err)
	}
	if err := task.ScanDir(tasksDir, "demo", store, scheduler); err != nil {
		t.Fatal(err)
	}
	if len(scheduler.Tasks()) != 0 {
		t.Fatalf("deleted task remained scheduled: %#v", scheduler.Tasks())
	}
	stored, err := store.Tasks().All()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Enabled {
		t.Fatalf("deleted task should remain in DB disabled, got %#v", stored)
	}
}

func TestWatcherRescansTaskDirectoryUntilClosed(t *testing.T) {
	root := t.TempDir()
	tasksDir := filepath.Join(root, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	scheduler := task.NewScheduler(func(ctx context.Context, t model.Task) error { return nil })
	watcher := task.NewWatcher(tasksDir, "demo", store, scheduler, 10*time.Millisecond)
	watcher.Start(context.Background())
	defer watcher.Close()

	if err := os.WriteFile(filepath.Join(tasksDir, "dynamic.yaml"), []byte(`
name: Dynamic
cron: "0 9 * * *"
target_type: p2p
target_id: ou_user
send_output: true
prompt: hello
`), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		if len(scheduler.Tasks()) == 1 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("watcher did not schedule dynamic task: %#v", scheduler.Tasks())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestScanDirBadYAMLDoesNotBlockValidTask(t *testing.T) {
	root := t.TempDir()
	tasksDir := filepath.Join(root, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "bad.yaml"), []byte("name: [bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "good.yaml"), []byte(`
name: Good
cron: "0 9 * * *"
target_type: p2p
target_id: ou_user
send_output: true
prompt: hello
`), 0o644); err != nil {
		t.Fatal(err)
	}
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	scheduler := task.NewScheduler(func(ctx context.Context, t model.Task) error { return nil })
	if err := task.ScanDir(tasksDir, "demo", store, scheduler); err == nil {
		t.Fatal("ScanDir should report bad YAML")
	}
	if got := scheduler.Tasks(); len(got) != 1 || got[0].ID != "demo/good" {
		t.Fatalf("valid task was not scheduled despite bad YAML: %#v", got)
	}
}

func TestScanDirParseErrorKeepsLastKnownGoodSchedule(t *testing.T) {
	root := t.TempDir()
	tasksDir := filepath.Join(root, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(tasksDir, "daily.yaml")
	if err := os.WriteFile(taskPath, []byte(`
name: Daily
cron: "0 9 * * *"
target_type: p2p
target_id: ou_user
send_output: true
prompt: hello
`), 0o644); err != nil {
		t.Fatal(err)
	}
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	scheduler := task.NewScheduler(func(ctx context.Context, t model.Task) error { return nil })
	if err := task.ScanDir(tasksDir, "demo", store, scheduler); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath, []byte("name: [bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := task.ScanDir(tasksDir, "demo", store, scheduler); err == nil {
		t.Fatal("ScanDir should report parse error")
	}
	if got := scheduler.Tasks(); len(got) != 1 || got[0].ID != "demo/daily" {
		t.Fatalf("parse error removed last-known-good scheduled task: %#v", got)
	}
	stored, _ := store.Tasks().All()
	if len(stored) != 1 || !stored[0].Enabled {
		t.Fatalf("parse error disabled stored task: %#v", stored)
	}
}
