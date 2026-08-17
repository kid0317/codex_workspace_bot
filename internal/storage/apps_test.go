package storage_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
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

func TestCreateAppUsesInsertOnlyAndPreservesEnabled(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &storage.Store{DB: db}
	app := storage.App{
		Name: "new-app", FeishuAppID: "cli_new", FeishuAppSecret: "secret",
		WorkspaceDir: "/tmp/workspace", WorkspaceMode: "work", Model: "model", ReasoningEffort: "high", Enabled: true,
	}
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO apps
		(id, name, feishu_app_id, feishu_app_secret, workspace_dir, workspace_mode, model, reasoning_effort, enabled)
		VALUES (UUID(), ?, ?, ?, ?, ?, ?, ?, ?)`)).
		WithArgs(app.Name, app.FeishuAppID, app.FeishuAppSecret, app.WorkspaceDir, app.WorkspaceMode, app.Model, app.ReasoningEffort, app.Enabled).
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := store.CreateApp(context.Background(), app); err != nil {
		t.Fatalf("CreateApp() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateAppReturnsDuplicateFailureWithoutUpdate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &storage.Store{DB: db}
	app := storage.App{Name: "duplicate", FeishuAppID: "cli_duplicate", Enabled: true}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO apps")).
		WithArgs(app.Name, app.FeishuAppID, app.FeishuAppSecret, app.WorkspaceDir, app.WorkspaceMode, app.Model, app.ReasoningEffort, app.Enabled).
		WillReturnError(errors.New("Error 1062: Duplicate entry"))

	err = store.CreateApp(context.Background(), app)
	if err == nil || !strings.Contains(err.Error(), "create app") || !strings.Contains(err.Error(), "Duplicate") {
		t.Fatalf("CreateApp() error = %v, want wrapped duplicate failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateAppUsesStrictUpdateAndRequiresExistingName(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &storage.Store{DB: db}
	app := storage.App{
		Name: "existing", FeishuAppID: "cli_changed", FeishuAppSecret: "changed-secret",
		WorkspaceDir: "/tmp/changed", WorkspaceMode: "work", Model: "new-model", ReasoningEffort: "medium", Enabled: false,
	}
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE apps SET
		feishu_app_id=?, feishu_app_secret=?, workspace_dir=?, workspace_mode=?, model=?, reasoning_effort=?, enabled=?
		WHERE name=?`)).
		WithArgs(app.FeishuAppID, app.FeishuAppSecret, app.WorkspaceDir, app.WorkspaceMode, app.Model, app.ReasoningEffort, app.Enabled, app.Name).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = store.UpdateApp(context.Background(), app)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("UpdateApp() error = %v, want not-found error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
