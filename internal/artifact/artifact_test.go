package artifact_test

import (
	"os"
	"strings"
	"testing"
)

func TestGitignoreCoversRuntimeArtifacts(t *testing.T) {
	data, err := os.ReadFile("../../.gitignore")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, pattern := range []string{"/runtime/", "/workspaces/", "*.db", "*.db-shm", "*.db-wal", "*.log", "*.pid"} {
		if !strings.Contains(text, pattern) {
			t.Fatalf(".gitignore missing %s", pattern)
		}
	}
}

func TestStory06EvidenceArtifactsExist(t *testing.T) {
	required := map[string][]string{
		"../../scripts/story06_smoke.sh": {
			"/health",
			"/debug/dispatch",
			"/debug/task/run",
			"sqlite3",
			"git status --ignored --short",
			"debug disabled",
		},
		"../../docs/evidence/story06/README.md": {
			"health",
			"debug dispatch",
			"task run",
			"SQLite",
			"artifact cleanliness",
			"debug disabled",
		},
		"../../start.sh": {
			"DEBUG_TOKEN",
			"config.yaml.template",
			"go run ./cmd/server",
		},
		"../../build.sh": {
			"go test ./...",
			"go vet ./...",
			"go build",
			"dist",
		},
	}
	for path, needles := range required {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing evidence artifact %s: %v", path, err)
		}
		text := string(data)
		for _, needle := range needles {
			if !strings.Contains(text, needle) {
				t.Fatalf("%s missing %q", path, needle)
			}
		}
	}
}
