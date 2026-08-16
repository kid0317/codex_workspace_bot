package secretfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadTrimsOneLineTerminator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("feishu-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "feishu-secret" {
		t.Fatalf("secret = %q", got)
	}
}

func TestReadRejectsMultilineSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("multiline secret accepted")
	}
}

func TestReadRejectsEmptySecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("empty secret accepted")
	}
}
