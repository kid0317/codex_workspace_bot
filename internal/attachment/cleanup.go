package attachment

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/kid0317/codex-workspace-bot/internal/storage"
)

// CleanupCandidate is kept as an alias so cleanup has no duplicate persistence
// model while callers can use a small, filesystem-safe contract.
type CleanupCandidate = storage.ExpiredAttachment

type CleanupStore interface {
	ListExpiredAttachments(context.Context, int) ([]storage.ExpiredAttachment, error)
	ClaimAttachmentDeletion(context.Context, string) (bool, error)
	CompleteAttachmentDeletion(context.Context, string) error
	RestoreAttachmentDeletion(context.Context, string, string) error
}

// Cleaner deletes only retention-expired attachment directories after the
// record is durably transitioned to deleting. The personal-local product has
// no workspace or directory allowlist for attachment storage.
type Cleaner struct {
	Store CleanupStore
	Limit int
}

func (c Cleaner) Run(ctx context.Context) (int, error) {
	if c.Store == nil {
		return 0, errors.New("attachment cleanup configuration is invalid")
	}
	if c.Limit <= 0 {
		c.Limit = 100
	}
	candidates, err := c.Store.ListExpiredAttachments(ctx, c.Limit)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	for _, candidate := range candidates {
		claimed, err := c.Store.ClaimAttachmentDeletion(ctx, candidate.ID)
		if err != nil {
			return cleaned, err
		}
		if !claimed {
			continue
		}
		if err := c.remove(candidate); err != nil {
			if restoreErr := c.Store.RestoreAttachmentDeletion(ctx, candidate.ID, candidate.State); restoreErr != nil {
				return cleaned, restoreErr
			}
			continue
		}
		if err := c.Store.CompleteAttachmentDeletion(ctx, candidate.ID); err != nil {
			return cleaned, err
		}
		cleaned++
	}
	return cleaned, nil
}

func (c Cleaner) remove(candidate CleanupCandidate) error {
	if candidate.WorkspaceDir == "" || candidate.RelativePath == "" {
		return nil
	}
	payload := localAttachmentPath(candidate.WorkspaceDir, candidate.RelativePath)
	leaf := filepath.Base(payload)
	if leaf != "payload" && leaf != safeDisplayName(candidate.OriginalNameSafe) {
		return errors.New("attachment payload name is invalid")
	}
	dir := filepath.Dir(payload)
	if filepath.Base(dir) != candidate.ID {
		return errors.New("attachment directory ID is invalid")
	}
	if filepath.Base(filepath.Dir(dir)) != candidate.SessionID {
		return errors.New("attachment directory session ID is invalid")
	}
	if err := os.RemoveAll(dir); err != nil {
		return errors.New("remove attachment payload")
	}
	return nil
}
