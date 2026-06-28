package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/db"
)

type Watcher struct {
	tasksDir  string
	appID     string
	store     *db.Store
	scheduler *Scheduler
	interval  time.Duration
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

func NewWatcher(tasksDir, appID string, store *db.Store, scheduler *Scheduler, interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = time.Minute
	}
	return &Watcher{tasksDir: tasksDir, appID: appID, store: store, scheduler: scheduler, interval: interval}
}

func (w *Watcher) Start(ctx context.Context) {
	if w.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		_ = ScanDir(w.tasksDir, w.appID, w.store, w.scheduler)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				_ = ScanDir(w.tasksDir, w.appID, w.store, w.scheduler)
			}
		}
	}()
}

func (w *Watcher) Close() {
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
	if w.scheduler != nil {
		w.scheduler.Close()
	}
}

func ScanDir(tasksDir, appID string, store *db.Store, scheduler *Scheduler) error {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		if os.IsNotExist(err) {
			if scheduler != nil {
				removeAppTasksNotSeen(appID, map[string]bool{}, scheduler)
			}
			return nil
		}
		return err
	}
	seen := map[string]bool{}
	var scanErr error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		loaded, err := LoadYAML(filepath.Join(tasksDir, entry.Name()), appID)
		if err != nil {
			scanErr = errors.Join(scanErr, err)
			continue
		}
		if err := store.Tasks().Save(loaded); err != nil {
			return err
		}
		seen[loaded.ID] = true
		if scheduler == nil {
			continue
		}
		if loaded.Enabled {
			if err := scheduler.Add(loaded); err != nil {
				return err
			}
		} else {
			scheduler.Remove(loaded.ID)
		}
	}
	if scanErr != nil {
		return scanErr
	}
	if scheduler != nil {
		removeAppTasksNotSeen(appID, seen, scheduler)
	}
	keepIDs := make([]string, 0, len(seen))
	for id := range seen {
		keepIDs = append(keepIDs, id)
	}
	if err := store.Tasks().DisableMissing(appID, keepIDs); err != nil {
		return err
	}
	return nil
}

func removeAppTasksNotSeen(appID string, seen map[string]bool, scheduler *Scheduler) {
	for _, scheduled := range scheduler.Tasks() {
		if scheduled.AppID == appID && !seen[scheduled.ID] {
			scheduler.Remove(scheduled.ID)
		}
	}
}
