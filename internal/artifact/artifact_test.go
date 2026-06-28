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
