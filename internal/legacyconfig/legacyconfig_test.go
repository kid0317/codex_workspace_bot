package legacyconfig_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/legacyconfig"
)

func TestLoadAppReturnsOnlyRequestedLegacyApp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `apps:
  - id: "other"
    feishu_app_id: "cli_other"
    feishu_app_secret: "other-secret"
    workspace_dir: "/tmp/other"
  - id: "health-assistant"
    feishu_app_id: "cli_health"
    feishu_app_secret: "health-secret"
    workspace_dir: "/root/health"
    claude:
      permission_mode: "acceptEdits"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	app, err := legacyconfig.LoadApp(path, "health-assistant")
	if err != nil {
		t.Fatalf("LoadApp() error = %v", err)
	}
	if app.Name != "health-assistant" || app.FeishuAppID != "cli_health" || app.WorkspaceDir != "/root/health" {
		t.Fatalf("LoadApp() = %#v", app)
	}
	if app.WorkspaceMode != "work" || app.Model != "gpt-5.6-terra" || app.ReasoningEffort != "high" {
		t.Fatalf("unexpected migration defaults: %#v", app)
	}
}

func TestLoadAppPreservesCompanionWorkspaceMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `apps:
  - id: "xh_yibu"
    feishu_app_id: "cli_xh"
    feishu_app_secret: "xh-secret"
    workspace_dir: "/root/xh_yibu"
    workspace_mode: "companion"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	app, err := legacyconfig.LoadApp(path, "xh_yibu")
	if err != nil {
		t.Fatalf("LoadApp() error = %v", err)
	}
	if app.WorkspaceMode != "companion" {
		t.Fatalf("WorkspaceMode = %q, want companion", app.WorkspaceMode)
	}
}
