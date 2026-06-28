package artifact_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/db"
)

func TestRequiredStoryFixturesExistAndLegacyDBOpens(t *testing.T) {
	root := filepath.Join("..", "..", "testdata")
	required := []string{
		"legacy/config.redacted.yaml",
		"legacy/bot.db",
		"legacy/events/p2p_text.json",
		"legacy/events/group_thread_text.json",
		"legacy/workspace_minimal/CLAUDE.md",
		"legacy/workspace_minimal/.claude/story-state-SAMPLE.local.md",
		"legacy/tasks/user_reply.yaml",
		"legacy/tasks/borrow_channel_post_archive.yaml",
		"output_filter/dirty_messages.yaml",
		"telemetry/langfuse_dryrun_rows.jsonl",
		"chat_history/bot.db",
		"chat_history/SESSION_CONTEXT.md",
	}
	for _, name := range required {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("required fixture %s missing: %v", name, err)
		}
	}
	store, err := db.Open(filepath.Join(root, "legacy", "bot.db"))
	if err != nil {
		t.Fatalf("legacy bot.db should open: %v", err)
	}
	if count, err := store.Sessions().Count(); err != nil || count < 2 {
		t.Fatalf("legacy bot.db sessions = %d err=%v", count, err)
	}
}
