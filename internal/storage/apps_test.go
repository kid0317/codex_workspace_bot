package storage_test

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
)

func TestUpsertAppWritesCredentialAndRuntimeFields(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &storage.Store{DB: db}
	app := storage.App{
		Name: "health-assistant", FeishuAppID: "cli_health", FeishuAppSecret: "secret",
		WorkspaceDir: "/root/health", WorkspaceMode: "work", Model: "gpt-5-codex", ReasoningEffort: "high",
	}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO apps")).
		WithArgs(app.Name, app.FeishuAppID, app.FeishuAppSecret, app.WorkspaceDir, app.WorkspaceMode, app.Model, app.ReasoningEffort).
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := store.UpsertApp(context.Background(), app); err != nil {
		t.Fatalf("UpsertApp() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
