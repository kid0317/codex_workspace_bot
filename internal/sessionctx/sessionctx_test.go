package sessionctx_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/sessionctx"
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
