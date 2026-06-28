package task

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kid0317/codex-workspace-bot/internal/db"
	"github.com/kid0317/codex-workspace-bot/internal/engine"
	"github.com/kid0317/codex-workspace-bot/internal/feishu"
	"github.com/kid0317/codex-workspace-bot/internal/model"
	"github.com/kid0317/codex-workspace-bot/internal/session"
	"github.com/kid0317/codex-workspace-bot/internal/sessionctx"
)

type Runner struct {
	store        *db.Store
	engine       engine.Engine
	workspaceDir string
	managers     map[string]*session.Manager
}

func NewRunner(store *db.Store, eng engine.Engine, workspaceDir string) *Runner {
	return &Runner{store: store, engine: eng, workspaceDir: workspaceDir}
}

func NewRunnerWithManagers(store *db.Store, eng engine.Engine, workspaceDir string, managers map[string]*session.Manager) *Runner {
	return &Runner{store: store, engine: eng, workspaceDir: workspaceDir, managers: managers}
}

func (r *Runner) Run(ctx context.Context, t model.Task) error {
	switch t.Mode() {
	case model.TaskModeUserFacing:
		return r.runChannelTask(ctx, t, true)
	case model.TaskModeBorrowChannel:
		if err := r.runChannelTask(ctx, t, false); err != nil {
			return err
		}
		if t.PostArchive {
			return r.store.Sessions().ArchiveActive(buildChannelKey(t))
		}
		return nil
	case model.TaskModeSystem:
		return r.runSystemTask(ctx, t)
	default:
		return nil
	}
}

func (r *Runner) runSystemTask(ctx context.Context, t model.Task) error {
	slug := slugFromID(t.ID)
	writer := sessionctx.Writer{WorkspaceDir: r.workspaceDir}
	if _, err := writer.Write(sessionctx.Context{AppID: t.AppID, WorkspaceMode: "work", TaskID: t.ID, TaskName: t.Name, SystemSlug: slug, TasksDir: filepath.Join(r.workspaceDir, "tasks")}); err != nil {
		return err
	}
	stream, err := r.engine.SendTurn(ctx, engine.TurnRequest{Prompt: sessionctx.InjectRouting(t.Prompt, sessionctx.Context{AppID: t.AppID, TaskID: t.ID}), ThreadPolicy: engine.ThreadForceNew})
	if err != nil {
		return err
	}
	var events []engine.TurnEvent
	for stream.Next() {
		events = append(events, stream.Event())
	}
	if err := stream.Err(); err != nil {
		return err
	}
	if err := engine.ValidateEvents(events); err != nil {
		return err
	}
	for _, ev := range events {
		if ev.Type == engine.EventFailed || ev.Type == engine.EventInterrupted {
			return fmt.Errorf("system task engine turn %s: %s", ev.Type, ev.Error)
		}
	}
	now := time.Now()
	t.LastRunAt = &now
	return r.store.Tasks().Save(t)
}

func (r *Runner) runChannelTask(ctx context.Context, t model.Task, sendOutput bool) error {
	key := buildChannelKey(t)
	manager := r.managers[t.AppID]
	if manager == nil {
		return fmt.Errorf("task manager not found for app %s", t.AppID)
	}
	msg := feishu.IncomingMessage{
		AppID:          t.AppID,
		ChatType:       t.TargetType,
		ChatID:         t.TargetID,
		ChannelKey:     key,
		SenderID:       t.CreatedBy,
		MessageID:      "task:" + t.ID + ":" + uuid.NewString(),
		Prompt:         t.Prompt,
		SuppressOutput: !sendOutput,
		ForceNewThread: true,
		TaskID:         t.ID,
		TaskName:       t.Name,
		ReceiveID:      t.TargetID,
		ReceiveType:    receiveType(t.TargetType),
	}
	if err := manager.Dispatch(ctx, msg); err != nil {
		return err
	}
	now := time.Now()
	t.LastRunAt = &now
	return r.store.Tasks().Save(t)
}

func buildChannelKey(t model.Task) string {
	if t.TargetType == "p2p" {
		return "p2p:" + t.TargetID + ":" + t.AppID
	}
	return "group:" + t.TargetID + ":" + t.AppID
}

func receiveType(targetType string) string {
	if targetType == "p2p" {
		return "open_id"
	}
	return "chat_id"
}

func slugFromID(id string) string {
	if idx := strings.LastIndex(id, "/"); idx >= 0 {
		return id[idx+1:]
	}
	return id
}
