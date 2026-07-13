package appimport_test

import (
	"context"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/appimport"
	"github.com/kid0317/codex-workspace-bot/internal/legacyconfig"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
)

type recordingWriter struct{ app storage.App }

func (w *recordingWriter) UpsertApp(_ context.Context, app storage.App) error {
	w.app = app
	return nil
}

func TestImportWritesLegacyApp(t *testing.T) {
	writer := &recordingWriter{}
	legacyApp := legacyconfig.App{
		Name: "health-assistant", FeishuAppID: "cli_health", FeishuAppSecret: "secret",
		WorkspaceDir: "/root/health", WorkspaceMode: "work", Model: "gpt-5-codex", ReasoningEffort: "high",
	}
	if err := appimport.Import(context.Background(), writer, legacyApp); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if writer.app.Name != legacyApp.Name || writer.app.FeishuAppSecret != legacyApp.FeishuAppSecret {
		t.Fatalf("writer got %#v", writer.app)
	}
}
