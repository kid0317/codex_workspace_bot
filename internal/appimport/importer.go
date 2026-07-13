package appimport

import (
	"context"

	"github.com/kid0317/codex-workspace-bot/internal/legacyconfig"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
)

type AppWriter interface {
	UpsertApp(context.Context, storage.App) error
}

func Import(ctx context.Context, writer AppWriter, legacyApp legacyconfig.App) error {
	return writer.UpsertApp(ctx, storage.App{
		Name:            legacyApp.Name,
		FeishuAppID:     legacyApp.FeishuAppID,
		FeishuAppSecret: legacyApp.FeishuAppSecret,
		WorkspaceDir:    legacyApp.WorkspaceDir,
		WorkspaceMode:   legacyApp.WorkspaceMode,
		Model:           legacyApp.Model,
		ReasoningEffort: legacyApp.ReasoningEffort,
	})
}
