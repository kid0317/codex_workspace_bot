package sessionctx_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/db"
	"github.com/kid0317/codex-workspace-bot/internal/sessionctx"
	"gopkg.in/yaml.v3"
)

func TestWriterCreatesInteractiveAndSystemContextFiles(t *testing.T) {
	root := t.TempDir()
	w := sessionctx.Writer{WorkspaceDir: root}
	interactive, err := w.Write(sessionctx.Context{
		AppID: "demo", WorkspaceMode: "work", SessionID: "s1", ChannelKey: "p2p:oc:demo",
		ChatType: "p2p", ChatID: "oc", ReceiveID: "ou", ReceiveType: "open_id", MessageID: "m1", EngineThreadID: "thread",
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(interactive)
	if !strings.Contains(string(data), "channel_key: p2p:oc:demo") || !strings.Contains(string(data), "engine_thread_id: thread") {
		t.Fatalf("interactive context = %s", data)
	}
	system, err := w.Write(sessionctx.Context{AppID: "demo", WorkspaceMode: "work", TaskID: "demo/sys", TaskName: "sys", SystemSlug: "sys"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.ToSlash(system) != filepath.ToSlash(filepath.Join(root, "sessions", "_system", "sys", "SESSION_CONTEXT.md")) {
		t.Fatalf("system path = %s", system)
	}
}

func TestRoutingInjectionWrapsMetadata(t *testing.T) {
	got := sessionctx.InjectRouting("hello", sessionctx.Context{AppID: "demo", ChannelKey: "group:oc:demo"})
	if !strings.Contains(got, "<system_routing>") || !strings.Contains(got, "hello") {
		t.Fatalf("routing prompt = %s", got)
	}
}

func TestWriterRejectsUnsafeSystemSlug(t *testing.T) {
	root := t.TempDir()
	_, err := (sessionctx.Writer{WorkspaceDir: root}).Write(sessionctx.Context{SystemSlug: "../escape", TaskID: "demo/../escape"})
	if err == nil {
		t.Fatal("unsafe system slug should be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(root, "escape", "SESSION_CONTEXT.md")); !os.IsNotExist(statErr) {
		t.Fatalf("unsafe context was created outside system dir: %v", statErr)
	}
}

func TestChatHistoryFixtureCompatibility(t *testing.T) {
	store, err := db.Open(copyDBFixture(t, "../../testdata/chat_history/bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := store.Sessions().ByChannel("p2p:oc_legacy:legacy-app")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) == 0 {
		t.Fatal("chat history fixture has no legacy p2p session")
	}
	contextData, err := os.ReadFile("../../testdata/chat_history/SESSION_CONTEXT.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contextData), "channel_key: p2p:oc_legacy:legacy-app") {
		t.Fatalf("fixture context missing channel key: %s", contextData)
	}
	var expected []struct {
		Query             string `yaml:"query"`
		ExpectedSessionID string `yaml:"expected_session_id"`
	}
	data, err := os.ReadFile("../../testdata/chat_history/expected_outputs.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	if len(expected) == 0 || expected[0].ExpectedSessionID == "" {
		t.Fatalf("expected outputs fixture = %#v", expected)
	}
}

func copyDBFixture(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}
