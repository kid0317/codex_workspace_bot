package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/attachment"
	"github.com/kid0317/codex-workspace-bot/internal/codexapp"
	"github.com/kid0317/codex-workspace-bot/internal/config"
	"github.com/kid0317/codex-workspace-bot/internal/feishu"
	"github.com/kid0317/codex-workspace-bot/internal/feishuaction"
	projectlog "github.com/kid0317/codex-workspace-bot/internal/logging"
	"github.com/kid0317/codex-workspace-bot/internal/observability"
	"github.com/kid0317/codex-workspace-bot/internal/router"
	"github.com/kid0317/codex-workspace-bot/internal/schedule"
	"github.com/kid0317/codex-workspace-bot/internal/scheduleaction"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	configPath := flag.String("config", "config.yaml", "path to YAML config")
	flag.Parse()
	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load_config", "error", err)
		os.Exit(1)
	}
	level, err := projectlog.ParseLevel(cfg.Logging.Level)
	if err != nil {
		slog.Error("parse_log_level", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logManager, err := projectlog.New(cfg.Logging.Dir, level, time.Now())
	if err != nil {
		slog.Error("open_log_files", "error", err)
		os.Exit(1)
	}
	logManager.SetRetentionDays(cfg.Logging.RetentionDays)
	defer logManager.Close()
	slog.SetDefault(logManager.Logger())
	logManager.Start(ctx)
	store, err := storage.Open(context.Background(), cfg.Database.DSN())
	if err != nil {
		slog.Error("mysql_connect", "error", err)
		os.Exit(1)
	}
	store.SetPool(cfg.Database.MaxOpen, cfg.Database.MaxIdle)
	defer store.Close()
	if err := store.Migrate(context.Background(), filepath.Join("migrations")); err != nil {
		slog.Error("migration_apply", "error", err)
		os.Exit(1)
	}
	if reconciled, err := store.ReconcileAbandonedCompanionDeliveries(context.Background()); err != nil {
		slog.Error("companion_delivery_reconcile", "event", "companion_delivery_reconcile_failed", "error", err)
		os.Exit(1)
	} else if reconciled > 0 {
		logManager.WorkflowLogger().Warn("companion_delivery_abandoned_reconciled", "event", "companion_delivery_abandoned_reconciled", "batch_count", reconciled)
	}
	attachmentRetentionDeadline := time.Now().Add(time.Duration(cfg.Attachments.RetentionDays) * 24 * time.Hour)
	if reconciled, err := store.ReconcileInterruptedAttachments(context.Background(), attachmentRetentionDeadline); err != nil {
		slog.Error("attachment_reconcile", "event", "attachment_reconcile_failed", "error", err)
		os.Exit(1)
	} else if reconciled > 0 {
		logManager.WorkflowLogger().Warn("attachment_abandoned_reconciled", "event", "attachment_abandoned_reconciled", "attachment_count", reconciled)
	}
	attachmentCleaner := attachment.Cleaner{Store: store}
	if cleaned, err := attachmentCleaner.Run(context.Background()); err != nil {
		slog.Error("attachment_cleanup", "event", "attachment_cleanup_failed", "error", err)
		os.Exit(1)
	} else if cleaned > 0 {
		logManager.WorkflowLogger().Info("attachment_retention_cleaned", "event", "attachment_retention_cleaned", "attachment_count", cleaned)
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if cleaned, cleanupErr := attachmentCleaner.Run(ctx); cleanupErr != nil {
					slog.Error("attachment_cleanup", "event", "attachment_cleanup_failed", "error", cleanupErr)
				} else if cleaned > 0 {
					logManager.WorkflowLogger().Info("attachment_retention_cleaned", "event", "attachment_retention_cleaned", "attachment_count", cleaned)
				}
			}
		}
	}()
	apps, err := store.ListEnabledApps(context.Background())
	if err != nil {
		slog.Error("load_enabled_apps", "error", err)
		os.Exit(1)
	}
	if len(apps) == 0 {
		slog.Error("load_enabled_apps", "error", "no enabled applications")
		os.Exit(1)
	}
	for _, app := range apps {
		if info, err := os.Stat(app.WorkspaceDir); err != nil || !info.IsDir() {
			slog.Error("validate_app_workspace", "app_id", app.ID, "error", "workspace_dir is unavailable")
			os.Exit(1)
		}
	}
	var telemetry *observability.Recorder
	observabilityState := "disabled"
	if cfg.Observability.Langfuse.Enabled && cfg.Observability.Langfuse.ProjectID != "" && cfg.Observability.Langfuse.ProjectBindingNonce != "" && cfg.Observability.Langfuse.ProjectBindingVerified {
		telemetry, err = observability.NewOTLP(ctx, observability.ExportConfig{
			BaseURL: cfg.Observability.Langfuse.BaseURL, PublicKey: cfg.Observability.Langfuse.PublicKey, SecretKey: cfg.Observability.Langfuse.SecretKey,
			ExportTimeoutSeconds: cfg.Observability.Langfuse.ExportTimeoutSeconds, MaxQueueSize: cfg.Observability.Langfuse.MaxQueueSize,
			ProjectID: cfg.Observability.Langfuse.ProjectID, Environment: cfg.Observability.Langfuse.Environment,
		})
		if err != nil {
			observabilityState = "degraded"
			slog.Warn("observability_config_invalid", "event", "observability_config_invalid", "error", err)
		} else {
			observabilityState = "ready"
			defer func() {
				flushCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Observability.Langfuse.ExportTimeoutSeconds)*time.Second)
				defer cancel()
				if shutdownErr := telemetry.Shutdown(flushCtx); shutdownErr != nil {
					slog.Warn("observability_shutdown", "event", "observability_shutdown_failed", "error", shutdownErr)
				}
			}()
		}
	} else if cfg.Observability.Langfuse.Enabled {
		observabilityState = "awaiting_project_binding"
		slog.Warn("observability_project_binding_required", "event", "observability_project_binding_required")
	}
	runtime := codexapp.NewRuntime(codexapp.Config{
		Command: cfg.Codex.Command, RPCTimeout: time.Duration(cfg.Codex.RPCTimeoutSeconds) * time.Second,
		TurnTimeout: time.Duration(cfg.Codex.TurnTimeoutSeconds) * time.Second, IdleTimeout: time.Duration(cfg.Codex.IdleTimeoutSeconds) * time.Second,
		Grace: time.Duration(cfg.Codex.GraceSeconds) * time.Second, Debug: cfg.Logging.Level == "debug", DebugDir: cfg.Logging.Dir,
	})
	if err := runtime.Start(ctx); err != nil {
		slog.Error("codex_app_server_boot", "error", err)
		os.Exit(1)
	}
	defer runtime.Close()
	slog.Info("server_ready", "event", "migration_applied", "enabled_app_count", len(apps))
	outputs := make(map[string]*feishu.Sender, len(apps))
	downloaders := make(map[string]attachment.Downloader, len(apps))
	actionClients := make(map[string]feishuaction.Client, len(apps))
	for _, app := range apps {
		sender := feishu.NewSender(app.FeishuAppID, app.FeishuAppSecret, cfg.FeishuActions.DefaultDocFolderToken)
		outputs[app.ID] = sender
		downloaders[app.ID] = sender
		actionClients[app.ID] = sender
	}
	processor := codexapp.Processor{Runtime: runtime, Store: store, UsageLedger: store, Observability: telemetry}
	var referenceProtector *attachment.ReferenceProtector
	var actions *feishuaction.Service
	if cfg.FeishuActions.Enabled {
		referenceProtector, err = attachment.NewReferenceProtector(cfg.Attachments.ResourceRefKeys)
		if err != nil {
			slog.Error("attachment_keyring", "error", err)
			os.Exit(1)
		}
		processor.Attachments = attachment.Service{Store: store, Opener: referenceProtector, Downloaders: downloaders, Processor: attachment.Processor{MaxFileBytes: cfg.Attachments.MaxFileBytes}, RootDir: cfg.Attachments.RootDir, Retention: time.Duration(cfg.Attachments.RetentionDays) * 24 * time.Hour}
		resultProtector, protectorErr := feishuaction.NewResultProtector(cfg.FeishuActions.ResultKeys)
		if protectorErr != nil {
			slog.Error("action_result_keyring", "error", protectorErr)
			os.Exit(1)
		}
		actionService := feishuaction.Service{Clients: actionClients, MaxFileBytes: cfg.Attachments.MaxFileBytes, Ledger: store, ResultLedger: store, ReplayLedger: store, Protector: resultProtector}
		actions = &actionService
	}
	var scheduleActions *scheduleaction.Service
	if cfg.FeishuActions.Enabled || cfg.Schedule.Enabled {
		processor.ToolHandlers = func(batch worker.Batch) codexapp.ToolHandler {
			feishuRoute := feishuaction.Route{AppID: batch.Runtime.ID, ChannelKey: batch.Key.String(), ChatGroupID: batch.Messages[0].ChatGroupID, Reply: batch.Messages[0].Reply, OwnerOpenID: batchActorOpenID(batch), WorkspaceDir: batch.Runtime.WorkspaceDir, OutboxDir: batch.Messages[0].AttachmentOutboxDir}
			scheduleRoute := scheduleaction.Route{AppID: batch.Runtime.ID, ChannelKey: batch.Key.String(), ChatGroupID: batch.Messages[0].ChatGroupID, Actor: batch.Messages[0].Actor}
			return func(ctx context.Context, call codexapp.ToolCall) (codexapp.ToolResult, error) {
				if isScheduleTool(call.Tool) {
					if scheduleActions == nil {
						return codexapp.ToolResult{Success: false, ContentItems: []codexapp.ToolContentItem{{Type: "inputText", Text: "schedule is unavailable"}}}, nil
					}
					return scheduleActions.Execute(ctx, scheduleRoute, call)
				}
				if actions == nil {
					return codexapp.ToolResult{Success: false, ContentItems: []codexapp.ToolContentItem{{Type: "inputText", Text: "tool is unavailable"}}}, nil
				}
				return actions.Execute(ctx, feishuRoute, call)
			}
		}
	}
	if cfg.Schedule.Enabled {
		processor.ToolCatalogVersion = codexapp.S06ToolCatalogVersion
	}
	workflowWriter := worker.WorkflowWriterFunc(func(ctx context.Context, event worker.CompanionWorkflowEvent) error {
		return logManager.WriteWorkflow(ctx, "companion_segment_delivery",
			slog.String("event", "companion_segment_delivery"),
			slog.String("batch_id", event.BatchID),
			slog.Any("source_trace_ids", event.SourceTraceIDs),
			slog.String("thread_id", event.ThreadID),
			slog.String("turn_id", event.TurnID),
			slog.Int("segment_index", event.SegmentIndex),
			slog.String("text_sha256", event.TextSHA256),
			slog.String("result", string(event.Result)),
			slog.String("reason", event.Reason),
			slog.String("message_id", event.MessageID),
			slog.Int("retry_count", event.RetryCount),
			slog.Time("at", event.At),
		)
	})
	lifecycle := &serverLifecycle{normal: storage.BatchLifecycle{Store: store}}
	manager := worker.NewManager(worker.Config{MaxWorkers: cfg.Worker.MaxWorkers, QueueDepth: cfg.Worker.QueueDepth, ProcessTimeout: time.Duration(cfg.Worker.InProcessTimeoutMinutes) * time.Minute, IdleTimeout: time.Duration(cfg.Worker.IdleTimeoutMinutes) * time.Minute, StopGrace: time.Duration(cfg.Worker.StopGraceSeconds) * time.Second, CompanionSegmentDelay: time.Duration(cfg.Streaming.CompanionSegmentDelayMS) * time.Millisecond, WorkflowWriter: workflowWriter}, func(batch worker.Batch) (worker.Output, error) {
		output := outputs[batch.Runtime.ID]
		if output == nil {
			return nil, fmt.Errorf("missing output adapter")
		}
		return output, nil
	}, processor.Process, lifecycle)
	manager.SetGoalRunController(runtime)
	defer manager.Close()
	if cfg.Schedule.Enabled {
		payloadKeys, ownerKeys, keyErr := scheduleKeyrings(cfg.Schedule)
		if keyErr != nil {
			slog.Error("schedule_keyring", "error", keyErr)
			os.Exit(1)
		}
		repository := &schedule.Repository{DB: store.DB, Protector: schedule.Protector{Payloads: payloadKeys, Owners: ownerKeys}, MaxEnabledTasksPerOwner: cfg.Schedule.MaxEnabledTasksPerOwner, MaxEnabledTasksPerApp: cfg.Schedule.MaxEnabledTasksPerApp}
		if migrated, migrateErr := repository.MigrateLegacyProtectedTaskData(ctx); migrateErr != nil {
			slog.Error("schedule_plaintext_migration", "event", "schedule_plaintext_migration_failed", "error", migrateErr)
			os.Exit(1)
		} else if migrated > 0 {
			slog.Info("schedule_plaintext_migration", "event", "schedule_plaintext_migrated", "records", migrated)
		}
		scheduleService := scheduleaction.Service{
			Store:          repository,
			AtomicCreate:   repository,
			AtomicList:     repository,
			AtomicUpdate:   repository,
			Cursors:        schedule.CursorCodec{Keys: ownerKeys},
			OwnerHMAC:      repository.Protector.OwnerHMAC,
			ArgumentsHMAC:  func(raw []byte) (string, error) { digest, _, err := ownerKeys.HMAC(raw); return digest, err },
			ScriptsEnabled: cfg.Scripts.Enabled,
		}
		scheduleActions = &scheduleService
		resultDelivery := schedule.ResultDeliveryDispatcher{Store: repository, SenderForApp: func(appID string) schedule.ResultDeliverySender { return outputs[appID] }}
		lifecycle.setScheduled(schedule.PromptRunLifecycle{Runs: repository, Delivery: resultDelivery, RunningLease: time.Duration(cfg.Worker.InProcessTimeoutMinutes)*time.Minute + 30*time.Second})
		if _, reconcileErr := repository.ReconcileInterruptedRuns(ctx, time.Now()); reconcileErr != nil {
			slog.Error("schedule_reconcile", "event", "schedule_reconcile_failed", "error", reconcileErr)
			os.Exit(1)
		}
		if _, reconcileErr := repository.ReconcileExpiredToolCalls(ctx, time.Now()); reconcileErr != nil {
			slog.Error("schedule_tool_reconcile", "event", "schedule_tool_reconcile_failed", "error", reconcileErr)
			os.Exit(1)
		}
		if _, reconcileErr := repository.ReconcileInFlightDeliveries(ctx, time.Now()); reconcileErr != nil {
			slog.Error("schedule_delivery_reconcile", "event", "schedule_delivery_reconcile_failed", "error", reconcileErr)
			os.Exit(1)
		}
		dispatcher := schedule.DispatcherMux{Prompts: schedule.PromptDispatcher{Repository: repository, Workers: manager}, Delivery: resultDelivery}
		if cfg.Scripts.Enabled {
			dispatcher.Scripts = schedule.ScriptExecutor{Repository: repository, Config: schedule.ScriptExecutionConfig{ShellPath: cfg.Scripts.ShellPath, Timeout: time.Duration(cfg.Scripts.TimeoutSeconds) * time.Second, MaxOutputBytes: cfg.Scripts.MaxOutputBytes}, Observability: telemetry}
			dispatcher.ScriptSlots = make(chan struct{}, cfg.Scripts.MaxConcurrent)
		}
		scheduler := schedule.Scheduler{Store: repository, Dispatch: dispatcher, TickInterval: time.Duration(cfg.Schedule.TickIntervalMS) * time.Millisecond, MisfireGrace: time.Duration(cfg.Schedule.MisfireGraceSeconds) * time.Second, MaxPromptDispatch: cfg.Schedule.MaxPromptDispatchPerTick}
		go func() {
			if scheduleErr := scheduler.Run(ctx); scheduleErr != nil && ctx.Err() == nil {
				slog.Error("scheduler_stopped", "event", "scheduler_stopped", "error", scheduleErr)
				stop()
			}
		}()
		slog.Info("scheduler_started", "event", "scheduler_started")
	}
	mux := http.NewServeMux()
	receivers := feishu.NewRegistry()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := store.DB.PingContext(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"receivers": receivers.Snapshot(), "observability": observabilityState})
	})
	server := &http.Server{Addr: cfg.Server.ListenAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	for _, app := range apps {
		app := app
		go func() {
			for ctx.Err() == nil {
				receivers.Set(app.ID, "connecting")
				handler := router.New(store, manager)
				handler.SetAppRuntime(router.App{ID: app.ID, Name: app.Name, WorkspaceDir: app.WorkspaceDir, WorkspaceMode: app.WorkspaceMode, Model: app.Model, Effort: app.ReasoningEffort})
				handler.SetRejectionSender(outputs[app.ID])
				handler.SetCommandResponder(outputs[app.ID])
				handler.SetAvailability(runtime)
				handler.SetSessionResetter(processor)
				handler.SetAccountReader(runtime)
				if referenceProtector != nil {
					handler.SetAttachmentProtector(referenceProtector)
				}
				receiver := feishu.NewReceiverWithStatus(app, handler, func(state string) { receivers.Set(app.ID, state) })
				slog.Info("feishu_connecting", "app_id", app.ID, "event", "feishu_connecting")
				err := receiver.Start(ctx)
				if ctx.Err() != nil {
					return
				}
				receivers.Set(app.ID, "reconnecting")
				slog.Error("feishu_receiver_stopped", "app_id", app.ID, "event", "feishu_reconnecting", "error", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
			}
		}()
	}
	go func() {
		slog.Info("http_listening", "addr", cfg.Server.ListenAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http_server", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		slog.Warn("worker_shutdown", "event", "worker_shutdown_incomplete", "error", err)
	}
	_ = server.Shutdown(shutdownCtx)
}

// The App Server publishes namespace dynamic tools by their local name in an
// item/tool/call (for example, "create" rather than "schedule.create").
// Accept both representations at this ingress boundary; scheduleaction keeps
// the persisted ledger identity canonical.
func isScheduleTool(tool string) bool {
	switch tool {
	case "schedule.list_own", "list_own", "schedule.create", "create", "schedule.update", "update":
		return true
	default:
		return false
	}
}

func scheduleKeyrings(cfg config.ScheduleConfig) (schedule.Keyring, schedule.Keyring, error) {
	payloadEntries := make([]schedule.Key, 0, len(cfg.PayloadKeys))
	for _, entry := range cfg.PayloadKeys {
		payloadEntries = append(payloadEntries, schedule.Key{Version: entry.Version, Material: entry.Key})
	}
	ownerEntries := make([]schedule.Key, 0, len(cfg.OwnerHMACKeys))
	for _, entry := range cfg.OwnerHMACKeys {
		ownerEntries = append(ownerEntries, schedule.Key{Version: entry.Version, Material: entry.Key})
	}
	payloads, err := schedule.NewKeyring(payloadEntries)
	if err != nil {
		return schedule.Keyring{}, schedule.Keyring{}, err
	}
	owners, err := schedule.NewKeyring(ownerEntries)
	if err != nil {
		return schedule.Keyring{}, schedule.Keyring{}, err
	}
	return payloads, owners, nil
}

// serverLifecycle keeps ordinary ingress persistence under storage while
// routing ScheduledPrompt terminal data to S06's separate run/outbox boundary.
// It preserves the existing companion lifecycle by forwarding that optional
// interface instead of replacing it with a schedule-only adapter.
type serverLifecycle struct {
	normal    worker.Lifecycle
	mu        sync.RWMutex
	scheduled worker.ScheduledRunResultLifecycle
}

func (l *serverLifecycle) setScheduled(scheduled worker.ScheduledRunResultLifecycle) {
	l.mu.Lock()
	l.scheduled = scheduled
	l.mu.Unlock()
}
func (l *serverLifecycle) currentScheduled() worker.ScheduledRunResultLifecycle {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.scheduled
}
func (l *serverLifecycle) MarkProcessing(ctx context.Context, ids []string) error {
	return l.normal.MarkProcessing(ctx, ids)
}
func (l *serverLifecycle) Complete(ctx context.Context, ids []string, cardID, content string, durationMS int64) error {
	return l.normal.Complete(ctx, ids, cardID, content, durationMS)
}
func (l *serverLifecycle) Fail(ctx context.Context, ids []string, code string, durationMS int64) error {
	return l.normal.Fail(ctx, ids, code, durationMS)
}
func (l *serverLifecycle) MarkScheduledRunning(ctx context.Context, runID, claimToken string) error {
	if scheduled := l.currentScheduled(); scheduled != nil {
		return scheduled.MarkScheduledRunning(ctx, runID, claimToken)
	}
	return fmt.Errorf("scheduled lifecycle is unavailable")
}
func (l *serverLifecycle) CompleteScheduledRun(ctx context.Context, runID, claimToken string, succeeded bool, code string, durationMS int64) error {
	if scheduled := l.currentScheduled(); scheduled != nil {
		return scheduled.CompleteScheduledRun(ctx, runID, claimToken, succeeded, code, durationMS)
	}
	return fmt.Errorf("scheduled lifecycle is unavailable")
}
func (l *serverLifecycle) CompleteScheduledRunResult(ctx context.Context, runID, claimToken string, succeeded bool, code string, durationMS int64, finalText, threadID, turnID string) error {
	if scheduled := l.currentScheduled(); scheduled != nil {
		return scheduled.CompleteScheduledRunResult(ctx, runID, claimToken, succeeded, code, durationMS, finalText, threadID, turnID)
	}
	return fmt.Errorf("scheduled lifecycle is unavailable")
}
func (l *serverLifecycle) DiscardScheduledRun(ctx context.Context, runID, claimToken, code string) error {
	if scheduled := l.currentScheduled(); scheduled != nil {
		if discarder, ok := scheduled.(worker.ScheduledRunDiscardLifecycle); ok {
			return discarder.DiscardScheduledRun(ctx, runID, claimToken, code)
		}
	}
	return fmt.Errorf("scheduled discard lifecycle is unavailable")
}
func (l *serverLifecycle) MarkCompanionDeliveryStarted(ctx context.Context, ids []string, batchID string) error {
	if companion, ok := l.normal.(worker.CompanionLifecycle); ok {
		return companion.MarkCompanionDeliveryStarted(ctx, ids, batchID)
	}
	return fmt.Errorf("companion lifecycle is unavailable")
}
func (l *serverLifecycle) CompleteCompanionDelivery(ctx context.Context, ids []string, summary worker.CompanionDeliverySummary) error {
	if companion, ok := l.normal.(worker.CompanionLifecycle); ok {
		return companion.CompleteCompanionDelivery(ctx, ids, summary)
	}
	return fmt.Errorf("companion lifecycle is unavailable")
}
func (l *serverLifecycle) FailCompanionDelivery(ctx context.Context, ids []string, batchID, code, firstMessageID string, durationMS int64) error {
	if companion, ok := l.normal.(worker.CompanionLifecycle); ok {
		return companion.FailCompanionDelivery(ctx, ids, batchID, code, firstMessageID, durationMS)
	}
	return fmt.Errorf("companion lifecycle is unavailable")
}

// batchActorOpenID returns an actor only for a non-empty, single-actor batch.
// A document action will fail closed when an unexpected mixed batch reaches
// this boundary, rather than selecting an owner from a reply target or model
// input.
func batchActorOpenID(batch worker.Batch) string {
	if len(batch.Messages) == 0 || batch.Messages[0].Actor.OpenID == "" {
		return ""
	}
	ownerOpenID := batch.Messages[0].Actor.OpenID
	for _, message := range batch.Messages[1:] {
		if message.Actor.OpenID != ownerOpenID {
			return ""
		}
	}
	return ownerOpenID
}
