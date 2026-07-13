package attachment

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/kid0317/codex-workspace-bot/internal/codexapp"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

type LifecycleStore interface {
	GetAttachmentForWorker(context.Context, string) (storage.AttachmentRecord, error)
	ClaimAttachment(context.Context, string, string, time.Time) (bool, error)
	CompleteAttachment(context.Context, storage.AttachmentCompletion) error
	FailAttachment(context.Context, string, string, string, time.Time) error
}

type ReferenceOpener interface {
	Open(appID, attachmentID, sourceMessageID string, ciphertext []byte, version int) (string, error)
}

// Service binds encrypted attachment metadata to a channel-owned worker
// attempt. It is intentionally the only place that sees decrypted resource
// keys, and returns only local input paths/manifests to Codex.
type Service struct {
	Store       LifecycleStore
	Opener      ReferenceOpener
	Processor   Processor
	Downloaders map[string]Downloader
	RootDir     string
	Retention   time.Duration
	Now         func() time.Time
}

func (s Service) Prepare(ctx context.Context, batch worker.Batch) ([]codexapp.TextInput, error) {
	if s.Store == nil || s.Opener == nil || s.RootDir == "" || len(batch.Messages) != 1 || !batch.Messages[0].HasRequiredAttachment || len(batch.Messages[0].AttachmentIDs) == 0 || batch.Runtime.ID == "" || batch.Runtime.WorkspaceDir == "" {
		return nil, fmt.Errorf("attachment batch is invalid")
	}
	if s.Retention <= 0 {
		s.Retention = 7 * 24 * time.Hour
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	processor := s.Processor
	if len(s.Downloaders) > 0 {
		processor.Downloader = s.Downloaders[batch.Runtime.ID]
	}
	sessionID := uuid.NewString()
	attemptID := uuid.NewString()
	outbox := filepath.Join(attachmentRoot(batch.Runtime.WorkspaceDir, s.RootDir), pathHash(batch.Runtime.ID), pathHash(batch.Key.String()), sessionID, "outbox")
	if err := os.MkdirAll(outbox, 0o777); err != nil {
		return nil, fmt.Errorf("create attachment session outbox: %w", err)
	}
	batch.Messages[0].AttachmentOutboxDir = outbox
	inputs := make([]codexapp.TextInput, 0, len(batch.Messages[0].AttachmentIDs))
	for _, id := range batch.Messages[0].AttachmentIDs {
		record, err := s.Store.GetAttachmentForWorker(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("load attachment: %w", err)
		}
		claimed, err := s.Store.ClaimAttachment(ctx, record.ID, attemptID, s.Now().Add(2*time.Minute))
		if err != nil {
			return nil, err
		}
		if !claimed {
			return nil, fmt.Errorf("attachment is not staged")
		}
		resourceKey, err := s.Opener.Open(batch.Runtime.ID, record.ID, record.SourceMessageID, record.SourceResourceRefEnc, record.SourceRefKeyVersion)
		if err != nil {
			_ = s.Store.FailAttachment(ctx, record.ID, attemptID, "attachment_invalid", s.Now().Add(s.Retention))
			return nil, fmt.Errorf("decrypt attachment reference: %w", err)
		}
		result, err := processor.Materialize(ctx, Input{WorkspaceDir: batch.Runtime.WorkspaceDir, RootDir: s.RootDir, AppID: batch.Runtime.ID, ChannelKey: batch.Key.String(), SessionID: sessionID, AttachmentID: record.ID, Kind: record.Kind, SourceMessageID: record.SourceMessageID, ResourceKey: resourceKey, OriginalName: record.OriginalNameSafe})
		if err != nil {
			code := "attachment_download_failed"
			if err == ErrTooLarge {
				code = "attachment_too_large"
			} else if errors.Is(err, ErrInvalid) {
				code = "attachment_invalid"
			}
			_ = s.Store.FailAttachment(ctx, record.ID, attemptID, code, s.Now().Add(s.Retention))
			return nil, err
		}
		if err := s.Store.CompleteAttachment(ctx, storage.AttachmentCompletion{ID: record.ID, AttemptID: attemptID, ObservedMIME: result.ObservedMIME, ByteSize: result.ByteSize, SHA256: result.SHA256, SessionID: sessionID, RelativePath: result.RelativePath, RetentionDeadline: s.Now().Add(s.Retention)}); err != nil {
			return nil, err
		}
		path := localAttachmentPath(batch.Runtime.WorkspaceDir, result.RelativePath)
		if record.Kind == storage.AttachmentImage {
			inputs = append(inputs,
				codexapp.TextInput{Type: "text", Text: fmt.Sprintf("User-uploaded image is available at local path: %s. Inspect this image and respond to the user.", path)},
				codexapp.TextInput{Type: "localImage", Path: path, Detail: "auto"},
			)
		} else {
			inputs = append(inputs, codexapp.TextInput{Type: "text", Text: fmt.Sprintf("File manifest: %s; local path: %s", result.DisplayName, path)})
		}
	}
	return inputs, nil
}
