package app

import (
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
	"github.com/kid0317/codex-workspace-bot/internal/session"
	"github.com/kid0317/codex-workspace-bot/internal/task"
	"github.com/kid0317/codex-workspace-bot/internal/workspace"
)

type Runtime struct {
	Config  config.Config
	Handler http.Handler
	Apps    map[string]*AppRuntime
}

type AppRuntime struct {
	Config  config.AppConfig
	Store   *db.Store
	Sender  *feishu.MockSender
	Manager *session.Manager
	Runner  *task.Runner
}

func Bootstrap(cfg config.Config) (*Runtime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	rt := &Runtime{Config: cfg, Apps: map[string]*AppRuntime{}}
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
		sender := feishu.NewMockSender()
		manager := session.NewManager(store, mockengine.New(), sender, session.Options{
			WorkspaceMode:     appCfg.WorkspaceMode,
			WorkspaceDir:      appCfg.WorkspaceDir,
			QueueSize:         cfg.Session.QueueSize,
			WorkerIdleTimeout: time.Duration(cfg.Session.WorkerIdleTimeoutMinutes) * time.Minute,
			ApprovalTimeout:   time.Duration(cfg.Approval.TimeoutSeconds) * time.Second,
			Guardrail: guardrail.New(guardrail.Config{
				MaxMessageBytes:  cfg.Guardrails.MaxMessageBytes,
				MaxOutputBytes:   cfg.Guardrails.MaxOutputBytes,
				MaxEventsPerTurn: cfg.Guardrails.MaxEventsPerTurn,
				MaxTurnDuration:  time.Duration(cfg.Guardrails.MaxTurnDurationMinutes) * time.Minute,
				AllowedChats:     appCfg.AllowedChats,
			}),
		})
		runner := task.NewRunnerWithManagers(store, mockengine.New(), appCfg.WorkspaceDir, managers)
		rt.Apps[appCfg.ID] = &AppRuntime{Config: appCfg, Store: store, Sender: sender, Manager: manager, Runner: runner}
		managers[appCfg.ID] = manager
		runners[appCfg.ID] = runner
		stores[appCfg.ID] = store
	}
	rt.Handler = debugapi.NewWithServices(cfg, debugapi.Services{Managers: managers, TaskRunners: runners, Stores: stores})
	return rt, nil
}
