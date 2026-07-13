package attachment

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/storage"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

type fakeDownloader struct{ body []byte }

func (d fakeDownloader) Download(context.Context, string, string, storage.AttachmentKind) (io.ReadCloser, string, error) {
	return io.NopCloser(bytes.NewReader(d.body)), "source-name", nil
}

type fakeLifecycleStore struct {
	record     storage.AttachmentRecord
	completed  storage.AttachmentCompletion
	failedCode string
}

func (s *fakeLifecycleStore) GetAttachmentForWorker(context.Context, string) (storage.AttachmentRecord, error) {
	return s.record, nil
}
func (s *fakeLifecycleStore) ClaimAttachment(context.Context, string, string, time.Time) (bool, error) {
	return true, nil
}
func (s *fakeLifecycleStore) CompleteAttachment(_ context.Context, completion storage.AttachmentCompletion) error {
	s.completed = completion
	return nil
}
func (s *fakeLifecycleStore) FailAttachment(_ context.Context, _ string, _ string, code string, _ time.Time) error {
	s.failedCode = code
	return nil
}

type fakeOpener struct{}

func (fakeOpener) Open(string, string, string, []byte, int) (string, error) {
	return "resource-key", nil
}

func TestServicePreparesImageAndFileInputsWithoutResourceKeys(t *testing.T) {
	workspace := t.TempDir()
	store := &fakeLifecycleStore{record: storage.AttachmentRecord{ID: "attachment-1", Kind: storage.AttachmentFile, SourceResourceRefEnc: []byte("ciphertext"), SourceRefKeyVersion: 1, SourceMessageID: "om-1", OriginalNameSafe: "report.txt"}}
	service := Service{Store: store, Opener: fakeOpener{}, Downloaders: map[string]Downloader{"app-1": fakeDownloader{body: []byte("contents")}}, Processor: Processor{MaxFileBytes: 30_000_000}, RootDir: ".codex-workspace-bot/attachments", Retention: time.Hour}
	batch := worker.Batch{Runtime: worker.AppRuntime{ID: "app-1", WorkspaceDir: workspace}, Key: worker.GroupKey("oc-1", "app-1"), ID: "batch-1", Messages: []worker.Message{{ID: "m-1", AttachmentIDs: []string{"attachment-1"}, HasRequiredAttachment: true}}}
	inputs, err := service.Prepare(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0].Type != "text" || !strings.Contains(inputs[0].Text, "report.txt") || strings.Contains(inputs[0].Text, "resource-key") {
		t.Fatalf("inputs=%#v", inputs)
	}
	if store.completed.RelativePath == "" || store.completed.SessionID == "" {
		t.Fatalf("completion=%#v", store.completed)
	}
	if batch.Messages[0].AttachmentOutboxDir == "" {
		t.Fatal("attachment batch did not retain its current-turn outbox")
	}
}

func TestServicePreparesImagePathManifestAndLocalImage(t *testing.T) {
	workspace := t.TempDir()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	store := &fakeLifecycleStore{record: storage.AttachmentRecord{ID: "attachment-image", Kind: storage.AttachmentImage, SourceResourceRefEnc: []byte("ciphertext"), SourceRefKeyVersion: 1, SourceMessageID: "om-image", OriginalNameSafe: "photo.png"}}
	service := Service{Store: store, Opener: fakeOpener{}, Downloaders: map[string]Downloader{"app-1": fakeDownloader{body: encoded.Bytes()}}, Processor: Processor{MaxFileBytes: 30_000_000}, RootDir: ".codex-workspace-bot/attachments", Retention: time.Hour}
	batch := worker.Batch{Runtime: worker.AppRuntime{ID: "app-1", WorkspaceDir: workspace}, Key: worker.GroupKey("oc-1", "app-1"), ID: "batch-image", Messages: []worker.Message{{ID: "m-image", AttachmentIDs: []string{"attachment-image"}, HasRequiredAttachment: true}}}
	inputs, err := service.Prepare(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	expectedPath := localAttachmentPath(workspace, store.completed.RelativePath)
	if len(inputs) != 2 || inputs[0].Type != "text" || !strings.Contains(inputs[0].Text, expectedPath) {
		t.Fatalf("image manifest inputs=%#v completion=%#v", inputs, store.completed)
	}
	if inputs[1].Type != "localImage" || inputs[1].Path != expectedPath || inputs[1].Detail != "auto" {
		t.Fatalf("image input=%#v completion=%#v", inputs[1], store.completed)
	}
}

func TestServiceMarksUndecodableImageAsInvalid(t *testing.T) {
	workspace := t.TempDir()
	store := &fakeLifecycleStore{record: storage.AttachmentRecord{ID: "attachment-image", Kind: storage.AttachmentImage, SourceResourceRefEnc: []byte("ciphertext"), SourceRefKeyVersion: 1, SourceMessageID: "om-image", OriginalNameSafe: "not-an-image.png"}}
	service := Service{Store: store, Opener: fakeOpener{}, Downloaders: map[string]Downloader{"app-1": fakeDownloader{body: []byte("not an image")}}, Processor: Processor{MaxFileBytes: 30_000_000}, RootDir: ".codex-workspace-bot/attachments", Retention: time.Hour}
	batch := worker.Batch{Runtime: worker.AppRuntime{ID: "app-1", WorkspaceDir: workspace}, Key: worker.GroupKey("oc-1", "app-1"), ID: "batch-image", Messages: []worker.Message{{ID: "m-image", AttachmentIDs: []string{"attachment-image"}, HasRequiredAttachment: true}}}

	_, err := service.Prepare(context.Background(), batch)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Prepare() error=%v, want ErrInvalid", err)
	}
	if store.failedCode != "attachment_invalid" {
		t.Fatalf("failedCode=%q, want attachment_invalid", store.failedCode)
	}
}

func TestMaterializeStreamsPayloadIntoSessionScopedAtomicFile(t *testing.T) {
	workspace := t.TempDir()
	processor := Processor{Downloader: fakeDownloader{body: []byte("ordinary file contents")}, MaxFileBytes: 30_000_000}
	result, err := processor.Materialize(context.Background(), Input{
		WorkspaceDir: workspace, RootDir: ".codex-workspace-bot/attachments", AppID: "app-1", ChannelKey: "group:oc-1:app-1", SessionID: "session-1", AttachmentID: "attachment-1",
		Kind: storage.AttachmentFile, SourceMessageID: "om-1", ResourceKey: "resource-key", OriginalName: "report.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(result.RelativePath) || result.ByteSize != int64(len("ordinary file contents")) || result.SHA256 == "" {
		t.Fatalf("result = %#v", result)
	}
	payload, err := os.ReadFile(filepath.Join(workspace, result.RelativePath))
	if err != nil || string(payload) != "ordinary file contents" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Join(workspace, result.RelativePath)), "payload.part")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("part file stat error=%v", err)
	}
}

func TestMaterializeAllowsAnAbsoluteAttachmentRoot(t *testing.T) {
	workspace := t.TempDir()
	attachmentRoot := t.TempDir()
	processor := Processor{Downloader: fakeDownloader{body: []byte("outside workspace")}, MaxFileBytes: 30_000_000}
	result, err := processor.Materialize(context.Background(), Input{
		WorkspaceDir: workspace, RootDir: attachmentRoot, AppID: "app-1", ChannelKey: "group:oc-1:app-1", SessionID: "session-1", AttachmentID: "attachment-1",
		Kind: storage.AttachmentFile, SourceMessageID: "om-1", ResourceKey: "resource-key", OriginalName: "report.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(result.RelativePath) {
		t.Fatalf("stored path=%q, want absolute path for an absolute configured root", result.RelativePath)
	}
	payload, err := os.ReadFile(result.RelativePath)
	if err != nil || string(payload) != "outside workspace" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
}

func TestMaterializeRejectsOverLimitAndRemovesPartFile(t *testing.T) {
	workspace := t.TempDir()
	processor := Processor{Downloader: fakeDownloader{body: []byte("12345")}, MaxFileBytes: 4}
	_, err := processor.Materialize(context.Background(), Input{
		WorkspaceDir: workspace, RootDir: ".codex-workspace-bot/attachments", AppID: "app-1", ChannelKey: "group:oc-1:app-1", SessionID: "session-1", AttachmentID: "attachment-1",
		Kind: storage.AttachmentFile, SourceMessageID: "om-1", ResourceKey: "resource-key",
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Materialize() error=%v, want ErrTooLarge", err)
	}
	if matches, globErr := filepath.Glob(filepath.Join(workspace, "**", "payload.part")); globErr != nil || len(matches) != 0 {
		t.Fatalf("part files=%v globErr=%v", matches, globErr)
	}
}
