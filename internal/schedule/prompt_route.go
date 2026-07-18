package schedule

import (
	"context"
	"database/sql"
	"fmt"
)

// PromptRoute is the scheduler-owned, fully validated route used to create a
// synthetic Worker message. It contains no prompt text and is never logged.
type PromptRoute struct {
	AppID, WorkspaceDir, WorkspaceMode, Model, Effort string
	ChatGroupID, ChatType, ChatID, OwnerOpenID        string
}

func (r Repository) LoadPromptRoute(ctx context.Context, run ClaimedRun) (PromptRoute, error) {
	if r.DB == nil {
		return PromptRoute{}, fmt.Errorf("schedule store database is required")
	}
	if run.ID == "" || run.TaskID == "" || run.Kind != TaskPrompt || run.TaskVersion == 0 {
		return PromptRoute{}, fmt.Errorf("scheduled prompt run identity is invalid")
	}
	var row struct {
		appID, workspaceDir, workspaceMode, model, effort string
		chatGroupID, chatType, chatID, ownerOpenID        string
	}
	err := r.DB.QueryRowContext(ctx, `SELECT a.id,a.workspace_dir,a.workspace_mode,a.model,a.reasoning_effort,cg.id,cg.chat_type,cg.chat_id,t.creator_open_id FROM scheduled_task_runs sr JOIN scheduled_tasks t ON t.id=sr.task_id JOIN apps a ON a.id=t.app_id JOIN chat_groups cg ON cg.id=t.chat_group_id WHERE sr.id=? AND sr.task_id=? AND sr.task_version=? AND sr.kind='prompt' AND sr.claim_token=? AND sr.state IN ('claimed','queued','running') AND a.enabled=TRUE AND cg.schedule_enabled=TRUE`, run.ID, run.TaskID, run.TaskVersion, run.ClaimToken).Scan(&row.appID, &row.workspaceDir, &row.workspaceMode, &row.model, &row.effort, &row.chatGroupID, &row.chatType, &row.chatID, &row.ownerOpenID)
	if err == sql.ErrNoRows {
		return PromptRoute{}, ErrRunClaimLost
	}
	if err != nil {
		return PromptRoute{}, fmt.Errorf("load scheduled prompt route: %w", err)
	}
	if row.ownerOpenID == "" {
		return PromptRoute{}, fmt.Errorf("scheduled prompt owner is missing")
	}
	return PromptRoute{AppID: row.appID, WorkspaceDir: row.workspaceDir, WorkspaceMode: row.workspaceMode, Model: row.model, Effort: row.effort, ChatGroupID: row.chatGroupID, ChatType: row.chatType, ChatID: row.chatID, OwnerOpenID: row.ownerOpenID}, nil
}
