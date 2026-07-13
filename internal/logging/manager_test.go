package logging_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/logging"
)

func TestManagerWritesSeparateCurrentFilesAndFiltersLevel(t *testing.T) {
	dir := t.TempDir()
	m, err := logging.New(dir, slog.LevelInfo, time.Date(2026, 7, 11, 10, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	m.Logger().Debug("hidden")
	m.Logger().Info("normal_event")
	m.WorkflowLogger().Info("workflow_event")
	if err := m.Sync(); err != nil {
		t.Fatal(err)
	}
	normal, err := os.ReadFile(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	wf, err := os.ReadFile(filepath.Join(dir, "server.log.wf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(normal), "normal_event") || strings.Contains(string(normal), "hidden") {
		t.Fatalf("normal log = %s", normal)
	}
	if !strings.Contains(string(wf), "workflow_event") || strings.Contains(string(wf), "normal_event") {
		t.Fatalf("wf log = %s", wf)
	}
}

func TestManagerWriteWorkflowReturnsHandlerResult(t *testing.T) {
	m, err := logging.New(t.TempDir(), slog.LevelInfo, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.WriteWorkflow(context.Background(), "workflow_write", slog.String("batch_id", "batch-1")); err != nil {
		t.Fatalf("WriteWorkflow() error = %v", err)
	}
	if err := m.Sync(); err != nil {
		t.Fatal(err)
	}
}

func TestManagerRotatesAndArchivesCompletedHours(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 7, 10, 23, 0, 0, 0, time.Local)
	m, err := logging.New(dir, slog.LevelInfo, start)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	m.Logger().Info("before_rotation")
	m.WorkflowLogger().Info("before_workflow_rotation")
	if err := m.Check(time.Date(2026, 7, 11, 0, 1, 0, 0, time.Local)); err != nil {
		t.Fatal(err)
	}
	if err := m.Sync(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"server-2026071023.log", "server-2026071023.log.wf"} {
		if _, err := os.Stat(filepath.Join(dir, "20260710", name)); err != nil {
			t.Fatalf("archived %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "server.log")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "server.log.wf")); err != nil {
		t.Fatal(err)
	}
}

func TestManagerBackgroundChecksUntilContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m, err := logging.New(t.TempDir(), slog.LevelInfo, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	m.Start(ctx)
}

func TestParseLevel(t *testing.T) {
	level, err := logging.ParseLevel("error")
	if err != nil || level != slog.LevelError {
		t.Fatalf("ParseLevel() = %v, %v", level, err)
	}
}

func TestManagerConcurrentWritesAndRotation(t *testing.T) {
	m, err := logging.New(t.TempDir(), slog.LevelInfo, time.Date(2026, 7, 11, 10, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				m.Logger().Info("concurrent")
			}
			done <- struct{}{}
		}()
	}
	if err := m.Check(time.Date(2026, 7, 11, 11, 1, 0, 0, time.Local)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	if err := m.Sync(); err != nil {
		t.Fatal(err)
	}
}
