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
	"syscall"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/attachment"
	"github.com/kid0317/codex-workspace-bot/internal/codexapp"
	"github.com/kid0317/codex-workspace-bot/internal/config"
	"github.com/kid0317/codex-workspace-bot/internal/feishu"
	"github.com/kid0317/codex-workspace-bot/internal/feishuaction"
	projectlog "github.com/kid0317/codex-workspace-bot/internal/logging"
	"github.com/kid0317/codex-workspace-bot/internal/router"
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
	processor := codexapp.Processor{Runtime: runtime, Store: store}
	var referenceProtector *attachment.ReferenceProtector
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
		actions := feishuaction.Service{Clients: actionClients, MaxFileBytes: cfg.Attachments.MaxFileBytes, Ledger: store, ResultLedger: store, ReplayLedger: store, Protector: resultProtector}
		processor.ToolHandlers = func(batch worker.Batch) codexapp.ToolHandler {
			route := feishuaction.Route{AppID: batch.Runtime.ID, ChannelKey: batch.Key.String(), ChatGroupID: batch.Messages[0].ChatGroupID, Reply: batch.Messages[0].Reply, WorkspaceDir: batch.Runtime.WorkspaceDir, OutboxDir: batch.Messages[0].AttachmentOutboxDir}
			return func(ctx context.Context, call codexapp.ToolCall) (codexapp.ToolResult, error) {
				return actions.Execute(ctx, route, call)
			}
		}
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
	manager := worker.NewManager(worker.Config{MaxWorkers: cfg.Worker.MaxWorkers, QueueDepth: cfg.Worker.QueueDepth, ProcessTimeout: time.Duration(cfg.Worker.InProcessTimeoutMinutes) * time.Minute, IdleTimeout: time.Duration(cfg.Worker.IdleTimeoutMinutes) * time.Minute, StopGrace: time.Duration(cfg.Worker.StopGraceSeconds) * time.Second, CompanionSegmentDelay: time.Duration(cfg.Streaming.CompanionSegmentDelayMS) * time.Millisecond, WorkflowWriter: workflowWriter}, func(batch worker.Batch) (worker.Output, error) {
		output := outputs[batch.Runtime.ID]
		if output == nil {
			return nil, fmt.Errorf("missing output adapter")
		}
		return output, nil
	}, processor.Process, storage.BatchLifecycle{Store: store})
	defer manager.Close()
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
		_ = json.NewEncoder(w).Encode(receivers.Snapshot())
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
				handler.SetAvailability(runtime)
				handler.SetSessionResetter(processor)
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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
}
