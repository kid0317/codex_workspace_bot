package task_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/model"
	"github.com/kid0317/codex-workspace-bot/internal/task"
)

func TestLoadYAMLAppliesDefaultsAndTargetMatrix(t *testing.T) {
	path := writeTask(t, "daily.yaml", `
name: Daily
cron: "0 9 * * *"
target_type: p2p
target_id: ou_user
prompt: hello
`)
	got, err := task.LoadYAML(path, "demo")
	if err != nil {
		t.Fatalf("LoadYAML() error = %v", err)
	}
	if got.ID != "demo/daily" || got.AppID != "demo" || !got.Enabled || !got.SendOutput {
		t.Fatalf("task defaults = %#v", got)
	}

	bad := writeTask(t, "bad.yaml", `
name: Bad
cron: "0 9 * * *"
send_output: true
prompt: invalid
`)
	if _, err := task.LoadYAML(bad, "demo"); err == nil {
		t.Fatal("LoadYAML() should reject send_output task without target")
	}

	disabled := writeTask(t, "disabled.yaml", `
name: Disabled
enabled: false
send_output: false
prompt: ok
`)
	got, err = task.LoadYAML(disabled, "demo")
	if err != nil {
		t.Fatalf("disabled system task error = %v", err)
	}
	if got.Enabled || got.Mode() != model.TaskModeSystem {
		t.Fatalf("disabled task = %#v", got)
	}
}

func writeTask(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
