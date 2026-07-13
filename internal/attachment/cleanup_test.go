package attachment

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type fakeCleanupStore struct {
	candidates []CleanupCandidate
	claimed    []string
	completed  []string
	restored   []string
}

func (s *fakeCleanupStore) ListExpiredAttachments(context.Context, int) ([]CleanupCandidate, error) {
	return s.candidates, nil
}

func (s *fakeCleanupStore) ClaimAttachmentDeletion(_ context.Context, id string) (bool, error) {
	s.claimed = append(s.claimed, id)
	return true, nil
}

func (s *fakeCleanupStore) CompleteAttachmentDeletion(_ context.Context, id string) error {
	s.completed = append(s.completed, id)
	return nil
}

func (s *fakeCleanupStore) RestoreAttachmentDeletion(_ context.Context, id, _ string) error {
	s.restored = append(s.restored, id)
	return nil
}

func TestCleanerRemovesOnlyExpiredControlledAttachmentDirectory(t *testing.T) {
	workspace := t.TempDir()
	relativePayload := filepath.Join(".codex-workspace-bot", "attachments", "app", "channel", "session", "attachment", "payload")
	payload := filepath.Join(workspace, relativePayload)
	if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &fakeCleanupStore{candidates: []CleanupCandidate{{ID: "attachment-1", State: "ready", WorkspaceDir: workspace, RelativePath: relativePayload}}}
	cleaner := Cleaner{Store: store}
	if cleaned, err := cleaner.Run(context.Background()); err != nil || cleaned != 1 {
		t.Fatalf("cleaner run = %d, %v", cleaned, err)
	}
	if _, err := os.Stat(filepath.Dir(payload)); !os.IsNotExist(err) {
		t.Fatalf("attachment directory remains, err=%v", err)
	}
	if len(store.claimed) != 1 || len(store.completed) != 1 || len(store.restored) != 0 {
		t.Fatalf("store calls claim=%v complete=%v restore=%v", store.claimed, store.completed, store.restored)
	}
}

func TestCleanerRemovesAttachmentFromAbsoluteConfiguredRoot(t *testing.T) {
	workspace := t.TempDir()
	root := t.TempDir()
	payload := filepath.Join(root, "app", "channel", "session", "attachment", "payload")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &fakeCleanupStore{candidates: []CleanupCandidate{{ID: "attachment-1", State: "failed", WorkspaceDir: workspace, RelativePath: payload}}}
	cleaner := Cleaner{Store: store}
	if cleaned, err := cleaner.Run(context.Background()); err != nil || cleaned != 1 {
		t.Fatalf("cleaner run = %d, %v", cleaned, err)
	}
	if _, err := os.Stat(filepath.Dir(payload)); !os.IsNotExist(err) {
		t.Fatalf("attachment directory remains, err=%v", err)
	}
	if len(store.claimed) != 1 || len(store.completed) != 1 || len(store.restored) != 0 {
		t.Fatalf("store calls claim=%v complete=%v restore=%v", store.claimed, store.completed, store.restored)
	}
}
