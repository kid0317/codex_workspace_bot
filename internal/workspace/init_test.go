package workspace_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/workspace"
)

func TestInitializerPreservesProtectedFilesAndGeneratesBridgeOnlyWhenMissing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("legacy rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".claude", "skills", "feishu_ops"), 0o755); err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(root, ".claude", "skills", "feishu_ops", "feishu.json")
	if err := os.WriteFile(secretPath, []byte(`{"token":"secret"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "story-state-SAMPLE.local.md"), []byte("state"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := workspace.Init(root, "demo-app")
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	for _, rel := range []string{".codex", ".codex/skills", ".claude/skills", "memory", "tasks", "sessions"} {
		if info, err := os.Stat(filepath.Join(root, rel)); err != nil || !info.IsDir() {
			t.Fatalf("expected dir %s to exist", rel)
		}
	}
	bridge, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bridge), "Codex Workspace Bridge") {
		t.Fatalf("bridge content = %q", string(bridge))
	}
	if got, _ := os.ReadFile(filepath.Join(root, "CLAUDE.md")); string(got) != "legacy rules" {
		t.Fatalf("CLAUDE.md overwritten: %q", string(got))
	}
	if mode := fileMode(t, secretPath); mode != 0o600 {
		t.Fatalf("feishu.json mode = %o, want 0600", mode)
	}
	if !report.GeneratedAgents {
		t.Fatal("expected GeneratedAgents=true")
	}

	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("custom agents"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err = workspace.Init(root, "demo-app")
	if err != nil {
		t.Fatalf("second Init() error = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "AGENTS.md")); string(got) != "custom agents" {
		t.Fatalf("existing AGENTS.md overwritten: %q", string(got))
	}
	if report.GeneratedAgents {
		t.Fatal("second init should not regenerate AGENTS.md")
	}
}

func TestInitializerReportsMalformedSkillsAsWarnings(t *testing.T) {
	root := t.TempDir()
	broken := filepath.Join(root, ".claude", "skills", "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "SKILL.md"), []byte("missing frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := workspace.Init(root, "demo-app")
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "demo-app") {
		t.Fatalf("warnings = %#v, want app-scoped malformed skill warning", report.Warnings)
	}
	if strings.Contains(report.Warnings[0], root) {
		t.Fatalf("warning leaked absolute workspace path: %s", report.Warnings[0])
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
