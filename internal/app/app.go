package app

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/config"
	"github.com/kid0317/codex-workspace-bot/internal/db"
	"github.com/kid0317/codex-workspace-bot/internal/debugapi"
	"github.com/kid0317/codex-workspace-bot/internal/feishu"
	"github.com/kid0317/codex-workspace-bot/internal/guardrail"
	"github.com/kid0317/codex-workspace-bot/internal/mockengine"
	"github.com/kid0317/codex-workspace-bot/internal/observability"
	"github.com/kid0317/codex-workspace-bot/internal/session"
	"github.com/kid0317/codex-workspace-bot/internal/task"
	"github.com/kid0317/codex-workspace-bot/internal/workspace"
)

type Runtime struct {
	Config  config.Config
	Handler http.Handler
	Apps    map[string]*AppRuntime
	cancel  context.CancelFunc
}

type AppRuntime struct {
	Config    config.AppConfig
	Store     *db.Store
	Sender    *feishu.MockSender
	Manager   *session.Manager
	Runner    *task.Runner
	Scheduler *task.Scheduler
	Watcher   *task.Watcher
}

func Bootstrap(cfg config.Config) (*Runtime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Engine.Type != "" && cfg.Engine.Type != "mock" {
		return nil, fmt.Errorf("engine.type %s 尚未实现", cfg.Engine.Type)
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt := &Runtime{Config: cfg, Apps: map[string]*AppRuntime{}, cancel: cancel}
	managers := map[string]*session.Manager{}
	runners := map[string]*task.Runner{}
	stores := map[string]*db.Store{}
	for _, appCfg := range cfg.Apps {
		if _, err := workspace.Init(appCfg.WorkspaceDir, appCfg.ID); err != nil {
			return nil, fmt.Errorf("初始化 workspace %s: %w", appCfg.ID, err)
		}
		store, err := db.Open(filepath.Join(appCfg.WorkspaceDir, "bot.db"))
		if err != nil {
			return nil, fmt.Errorf("打开 app DB %s: %w", appCfg.ID, err)
		}
		if err := store.Approvals().ExpirePendingBefore(appCfg.ID, time.Now()); err != nil {
			return nil, fmt.Errorf("过期 app pending approvals %s: %w", appCfg.ID, err)
		}
		sender := feishu.NewMockSender()
		manager := session.NewManager(store, mockengine.New(), sender, session.Options{
			WorkspaceMode:         appCfg.WorkspaceMode,
			WorkspaceDir:          appCfg.WorkspaceDir,
			QueueSize:             cfg.Session.QueueSize,
			WorkerIdleTimeout:     time.Duration(cfg.Session.WorkerIdleTimeoutMinutes) * time.Minute,
			DuplicateMessageTTL:   time.Duration(cfg.Session.DuplicateMessageTTLHours) * time.Hour,
			ApprovalTimeout:       time.Duration(cfg.Approval.TimeoutSeconds) * time.Second,
			PendingAttachmentTTL:  time.Duration(cfg.Attachments.PendingTTLMinutes) * time.Minute,
			AttachmentTempDir:     cfg.Attachments.TempDir,
			MaxPendingAttachments: cfg.Attachments.PendingMaxItems,
			MaxAttachmentBytes:    cfg.Attachments.MaxBytesPerAttachment,
			Guardrail: guardrail.New(guardrail.Config{
				MaxMessageBytes:  cfg.Guardrails.MaxMessageBytes,
				MaxOutputBytes:   cfg.Guardrails.MaxOutputBytes,
				MaxEventsPerTurn: cfg.Guardrails.MaxEventsPerTurn,
				MaxTurnDuration:  time.Duration(cfg.Guardrails.MaxTurnDurationMinutes) * time.Minute,
				AllowedChats:     appCfg.AllowedChats,
			}),
			Emitter: observability.SlogEmitter{},
		})
		runner := task.NewRunnerWithManagers(store, mockengine.New(), appCfg.WorkspaceDir, managers)
		scheduler := task.NewScheduler(runner.Run)
		storedTasks, err := store.Tasks().All()
		if err != nil {
			return nil, fmt.Errorf("恢复 app tasks %s: %w", appCfg.ID, err)
		}
		for _, stored := range storedTasks {
			if stored.AppID == appCfg.ID && stored.Enabled {
				if err := scheduler.Add(stored); err != nil {
					return nil, fmt.Errorf("恢复 app scheduler %s: %w", appCfg.ID, err)
				}
			}
		}
		_ = task.ScanDir(filepath.Join(appCfg.WorkspaceDir, "tasks"), appCfg.ID, store, scheduler)
		watcher := task.NewWatcher(filepath.Join(appCfg.WorkspaceDir, "tasks"), appCfg.ID, store, scheduler, time.Minute)
		watcher.Start(ctx)
		scheduler.Start(ctx, time.Minute)
		startAttachmentCleanup(ctx, store, appCfg.WorkspaceDir, cfg.Attachments.TempDir, cfg.Cleanup)
		rt.Apps[appCfg.ID] = &AppRuntime{Config: appCfg, Store: store, Sender: sender, Manager: manager, Runner: runner, Scheduler: scheduler, Watcher: watcher}
		managers[appCfg.ID] = manager
		runners[appCfg.ID] = runner
		stores[appCfg.ID] = store
	}
	rt.Handler = debugapi.NewWithServices(cfg, debugapi.Services{Managers: managers, TaskRunners: runners, Stores: stores})
	return rt, nil
}

func (r *Runtime) Close(ctx context.Context) error {
	if r.cancel != nil {
		r.cancel()
	}
	for _, app := range r.Apps {
		if app.Watcher != nil {
			app.Watcher.Close()
		}
		if app.Manager == nil {
			continue
		}
		if err := app.Manager.Close(ctx); err != nil {
			return err
		}
	}
	return nil
}

func startAttachmentCleanup(ctx context.Context, store *db.Store, workspaceDir, tempDir string, cleanup config.CleanupConfig) {
	ttlDays := cleanup.AttachmentsRetentionDays
	if ttlDays <= 0 {
		ttlDays = 7
	}
	interval := time.Minute
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				keys, err := store.Attachments().PendingChannelKeys()
				if err != nil {
					continue
				}
				for _, key := range keys {
					_ = session.CleanupExpiredAttachmentsWithRoots(store, key, int((time.Duration(ttlDays) * 24 * time.Hour).Seconds()), tempDir)
				}
				_ = workspaceDir
			}
		}
	}()
}
