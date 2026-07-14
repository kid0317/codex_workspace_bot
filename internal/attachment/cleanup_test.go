package attachment

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const (
	cleanupSessionID    = "00000000-0000-0000-0000-000000000001"
	cleanupAttachmentID = "00000000-0000-0000-0000-000000000002"
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
	relativePayload := filepath.Join(".codex-workspace-bot", "attachments", "app", "channel", cleanupSessionID, cleanupAttachmentID, "payload")
	payload := filepath.Join(workspace, relativePayload)
	if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &fakeCleanupStore{candidates: []CleanupCandidate{{ID: cleanupAttachmentID, State: "ready", WorkspaceDir: workspace, RelativePath: relativePayload, SessionID: cleanupSessionID, OriginalNameSafe: "report.txt"}}}
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
	payload := filepath.Join(root, "app", "channel", cleanupSessionID, cleanupAttachmentID, "payload")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &fakeCleanupStore{candidates: []CleanupCandidate{{ID: cleanupAttachmentID, State: "failed", WorkspaceDir: workspace, RelativePath: payload, SessionID: cleanupSessionID, OriginalNameSafe: "report.txt"}}}
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

func TestCleanerRemovesNamedAttachmentLeaf(t *testing.T) {
	workspace := t.TempDir()
	candidate := CleanupCandidate{ID: cleanupAttachmentID, State: "ready", WorkspaceDir: workspace, SessionID: cleanupSessionID, OriginalNameSafe: "../report.txt"}
	candidate.RelativePath = filepath.Join(".codex-workspace-bot", "attachments", "app", "channel", candidate.SessionID, candidate.ID, safeDisplayName(candidate.OriginalNameSafe))
	payload := filepath.Join(workspace, candidate.RelativePath)
	if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &fakeCleanupStore{candidates: []CleanupCandidate{candidate}}
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

func TestCleanerRefusesMismatchedAttachmentPath(t *testing.T) {
	for _, tc := range []struct {
		name, sessionID, attachmentID, leaf string
	}{
		{name: "leaf", sessionID: cleanupSessionID, attachmentID: cleanupAttachmentID, leaf: "unexpected.txt"},
		{name: "attachment ID", sessionID: cleanupSessionID, attachmentID: "00000000-0000-0000-0000-000000000003", leaf: "payload"},
		{name: "session ID", sessionID: "00000000-0000-0000-0000-000000000004", attachmentID: cleanupAttachmentID, leaf: "payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			candidate := CleanupCandidate{ID: cleanupAttachmentID, State: "ready", WorkspaceDir: workspace, SessionID: cleanupSessionID, OriginalNameSafe: "report.txt"}
			candidate.RelativePath = filepath.Join(".codex-workspace-bot", "attachments", "app", "channel", tc.sessionID, tc.attachmentID, tc.leaf)
			payload := filepath.Join(workspace, candidate.RelativePath)
			if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(payload, []byte("payload"), 0o600); err != nil {
				t.Fatal(err)
			}
			store := &fakeCleanupStore{candidates: []CleanupCandidate{candidate}}
			cleaner := Cleaner{Store: store}
			if cleaned, err := cleaner.Run(context.Background()); err != nil || cleaned != 0 {
				t.Fatalf("cleaner run = %d, %v", cleaned, err)
			}
			if _, err := os.Stat(filepath.Dir(payload)); err != nil {
				t.Fatalf("attachment directory missing, err=%v", err)
			}
			if len(store.claimed) != 1 || len(store.completed) != 0 || len(store.restored) != 1 {
				t.Fatalf("store calls claim=%v complete=%v restore=%v", store.claimed, store.completed, store.restored)
			}
		})
	}
}

func TestCleanerRefusesMalformedMatchingAttachmentIdentifiers(t *testing.T) {
	workspace := t.TempDir()
	candidate := CleanupCandidate{ID: "not-a-uuid", State: "ready", WorkspaceDir: workspace, SessionID: "not-a-session", OriginalNameSafe: "report.txt"}
	candidate.RelativePath = filepath.Join(".codex-workspace-bot", "attachments", "app", "channel", candidate.SessionID, candidate.ID, "payload")
	payload := filepath.Join(workspace, candidate.RelativePath)
	if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &fakeCleanupStore{candidates: []CleanupCandidate{candidate}}
	cleaner := Cleaner{Store: store}
	if cleaned, err := cleaner.Run(context.Background()); err != nil || cleaned != 0 {
		t.Fatalf("cleaner run = %d, %v", cleaned, err)
	}
	if _, err := os.Stat(filepath.Dir(payload)); err != nil {
		t.Fatalf("attachment directory missing, err=%v", err)
	}
	if len(store.claimed) != 1 || len(store.completed) != 0 || len(store.restored) != 1 {
		t.Fatalf("store calls claim=%v complete=%v restore=%v", store.claimed, store.completed, store.restored)
	}
}
