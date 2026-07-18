package storage_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/storage"
)

func TestTraceIDIsStableW3CFormat(t *testing.T) {
	first := storage.TraceID("app-1", "event-1")
	if first != storage.TraceID("app-1", "event-1") {
		t.Fatal("TraceID must be deterministic")
	}
	if len(first) != 32 {
		t.Fatalf("TraceID length = %d, want 32", len(first))
	}
	for _, r := range first {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			t.Fatalf("TraceID contains non-lowercase-hex rune %q", r)
		}
	}
}

func TestOpenRejectsUnavailableDatabase(t *testing.T) {
	ctx := context.Background()
	_, err := storage.Open(ctx, "root:bad@tcp(127.0.0.1:1)/missing?timeout=10ms")
	if err == nil {
		t.Fatal("Open() error = nil, want unavailable database error")
	}
}

func TestS05MigrationDefinesAttachmentAndActionPersistence(t *testing.T) {
	body, err := os.ReadFile("../../migrations/003_s05_attachments_actions.sql")
	if err != nil {
		t.Fatalf("read S05 migration: %v", err)
	}
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS attachments",
		"CREATE TABLE IF NOT EXISTS feishu_action_calls",
		"codex_toolset_version",
		"uk_feishu_action_call",
		"idx_attachments_cleanup",
	} {
		if !strings.Contains(string(body), required) {
			t.Fatalf("S05 migration missing %q", required)
		}
	}
}

func TestS06MigrationDefinesScheduledTaskBoundaries(t *testing.T) {
	body, err := os.ReadFile("../../migrations/005_s06_scheduled_tasks.sql")
	if err != nil {
		t.Fatalf("read S06 migration: %v", err)
	}
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS scheduled_tasks",
		"CREATE TABLE IF NOT EXISTS scheduled_task_runs",
		"CREATE TABLE IF NOT EXISTS scheduled_task_deliveries",
		"CREATE TABLE IF NOT EXISTS scheduled_task_tool_calls",
		"CREATE TABLE IF NOT EXISTS scheduled_script_definitions",
		"codex_tool_catalog_version",
		"catalog_upgrade_state",
		"schedule_enabled",
		"uk_scheduled_task_run_slot",
		"uk_scheduled_task_delivery",
		"uk_scheduled_task_tool_call",
		"idx_scheduled_tasks_due",
		"idx_scheduled_tasks_owner",
	} {
		if !strings.Contains(string(body), required) {
			t.Fatalf("S06 migration missing %q", required)
		}
	}
}

func TestS10MigrationMakesAttachmentPathsUTF8MB4(t *testing.T) {
	body, err := os.ReadFile("../../migrations/010_s05_attachment_relative_path_utf8mb4.sql")
	if err != nil {
		t.Fatalf("read S10 migration: %v", err)
	}
	for _, required := range []string{
		"ALTER TABLE attachments DROP INDEX uk_attachments_session_path",
		"MODIFY COLUMN relative_path VARCHAR(2048) CHARACTER SET utf8mb4 NULL",
		"ADD UNIQUE KEY uk_attachments_session_path (session_id, relative_path(700))",
	} {
		if !strings.Contains(string(body), required) {
			t.Fatalf("S10 migration missing %q", required)
		}
	}
}
