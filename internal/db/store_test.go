package db_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/db"
	"github.com/kid0317/codex-workspace-bot/internal/model"
)

func TestOpenMigratesAdditivelyAndMapsEngineThreadID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")
	store, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.Channels().Save(model.Channel{ChannelKey: "p2p:ou_1:demo", AppID: "demo", ChatType: "p2p", ChatID: "ou_1"}); err != nil {
		t.Fatal(err)
	}
	sess := model.Session{ID: "s1", ChannelKey: "p2p:ou_1:demo", EngineThreadID: "thread-old", Status: model.SessionActive}
	if err := store.Sessions().Save(sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Sessions().ByID("s1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.EngineThreadID != "thread-old" {
		t.Fatalf("EngineThreadID = %q", loaded.EngineThreadID)
	}
	if err := store.Sessions().SetEngineThreadID("s1", "thread-new"); err != nil {
		t.Fatal(err)
	}
	loaded, _ = store.Sessions().ByID("s1")
	if loaded.EngineThreadID != "thread-new" {
		t.Fatalf("updated EngineThreadID = %q", loaded.EngineThreadID)
	}
	if !store.HasColumn("sessions", "claude_session_id") {
		t.Fatal("legacy physical column claude_session_id missing")
	}
}

func TestOpenLegacySQLFixturePreservesRowsAndClaudeSessionID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")
	sqlBytes, err := os.ReadFile("../../testdata/legacy/bot.sql")
	if err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range strings.Split(string(sqlBytes), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if err := store.Exec(stmt); err != nil {
			t.Fatalf("exec legacy fixture statement %q: %v", stmt, err)
		}
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	channelCount, _ := store.Channels().Count()
	sessionCount, _ := store.Sessions().Count()
	if channelCount < 1 || sessionCount < 2 {
		t.Fatalf("legacy rows not preserved channels=%d sessions=%d", channelCount, sessionCount)
	}
	active, ok, err := store.Sessions().ActiveByChannel("p2p:oc_legacy:legacy-app")
	if err != nil || !ok {
		t.Fatalf("active legacy session missing ok=%v err=%v", ok, err)
	}
	if active.EngineThreadID != "legacy-thread-1" {
		t.Fatalf("EngineThreadID = %q", active.EngineThreadID)
	}
	if err := store.Sessions().SetEngineThreadID(active.ID, "legacy-thread-updated"); err != nil {
		t.Fatal(err)
	}
	active, _ = store.Sessions().ByID(active.ID)
	if active.EngineThreadID != "legacy-thread-updated" {
		t.Fatalf("updated EngineThreadID = %q", active.EngineThreadID)
	}
}
