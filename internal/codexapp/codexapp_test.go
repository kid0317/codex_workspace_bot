package codexapp_test

import (
	"path/filepath"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/codexapp"
)

func TestRuntimeSpecKeepsRuntimeStateOutsideWorkspaceAndIsPerApp(t *testing.T) {
	root := t.TempDir()
	a, err := codexapp.ResolveRuntime(codexapp.Config{RuntimeDir: filepath.Join(root, "runtime"), Topology: "per-app"}, codexapp.App{ID: "app-a", WorkspaceDir: filepath.Join(root, "workspaces", "a")})
	if err != nil {
		t.Fatal(err)
	}
	b, err := codexapp.ResolveRuntime(codexapp.Config{RuntimeDir: filepath.Join(root, "runtime"), Topology: "per-app"}, codexapp.App{ID: "app-b", WorkspaceDir: filepath.Join(root, "workspaces", "b")})
	if err != nil {
		t.Fatal(err)
	}
	if a.CWD != filepath.Join(root, "workspaces", "a") {
		t.Fatalf("cwd = %s", a.CWD)
	}
	if a.StateDir == b.StateDir || filepath.Dir(a.StateDir) != filepath.Join(root, "runtime") {
		t.Fatalf("state dirs not isolated: a=%s b=%s", a.StateDir, b.StateDir)
	}
	if filepath.Dir(a.SocketPath) != a.StateDir || filepath.Dir(a.AuthTokenPath) != a.StateDir {
		t.Fatalf("socket/auth must be process-local under state dir: %#v", a)
	}
}

func TestRuntimeSpecRejectsPathTraversalAppID(t *testing.T) {
	_, err := codexapp.ResolveRuntime(codexapp.Config{RuntimeDir: t.TempDir(), Topology: "per-app"}, codexapp.App{ID: "../bad", WorkspaceDir: t.TempDir()})
	if err == nil {
		t.Fatal("ResolveRuntime should reject traversal app id")
	}
}
