package schedule

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

type PromptWorker interface {
	Accept(context.Context, worker.Message) error
}

// PromptDispatcher enters Codex only by submitting an owner-preserving
// synthetic message to the original channel Worker FIFO.
type PromptDispatcher struct {
	Repository *Repository
	Workers    PromptWorker
	Now        func() time.Time
}

func (d PromptDispatcher) Dispatch(ctx context.Context, run ClaimedRun) error {
	if d.Repository == nil || d.Workers == nil {
		return fmt.Errorf("scheduled prompt dispatcher is not configured")
	}
	if run.Kind != TaskPrompt {
		return fmt.Errorf("scheduled prompt dispatcher received %q", run.Kind)
	}
	route, err := d.Repository.LoadPromptRoute(ctx, run)
	if err != nil {
		return err
	}
	if len(run.Payload) == 0 {
		return fmt.Errorf("scheduled prompt payload is missing")
	}
	key, reply := promptWorkerRoute(route)
	now := time.Now().UTC()
	if d.Now != nil {
		now = d.Now().UTC()
	}
	message := worker.Message{ID: uuid.NewString(), TraceID: run.TraceID, ChatGroupID: route.ChatGroupID, ChatType: route.ChatType, ChatID: route.ChatID, Key: key, Runtime: worker.AppRuntime{ID: route.AppID, WorkspaceDir: route.WorkspaceDir, WorkspaceMode: route.WorkspaceMode, Model: route.Model, Effort: route.Effort}, Reply: reply, Actor: worker.ActorPrincipal{OpenID: route.OwnerOpenID}, Query: string(run.Payload), ReceivedAt: now, ScheduledTaskID: run.TaskID, ScheduledTaskRunID: run.ID, ScheduledClaimToken: run.ClaimToken}
	if err := d.Repository.MarkRunQueued(ctx, run.ID, run.ClaimToken); err != nil {
		return fmt.Errorf("mark scheduled prompt queued: %w", err)
	}
	if err := d.Workers.Accept(ctx, message); err != nil {
		_ = d.Repository.CompleteRun(context.Background(), RunCompletion{RunID: run.ID, ClaimToken: run.ClaimToken, State: RunFailed, ErrorCode: "failed_enqueue"})
		return fmt.Errorf("enqueue scheduled prompt: %w", err)
	}
	return nil
}

func promptWorkerRoute(route PromptRoute) (worker.Key, worker.ReplyTarget) {
	if route.ChatType == "group" {
		return worker.GroupKey(route.ChatID, route.AppID), worker.ReplyTarget{ID: route.ChatID, Type: "chat_id"}
	}
	return worker.P2PKey(route.OwnerOpenID, route.AppID), worker.ReplyTarget{ID: route.OwnerOpenID, Type: "open_id"}
}
