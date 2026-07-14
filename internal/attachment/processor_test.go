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
	"unicode/utf8"

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

func temporaryAttachmentPath(workspace string, input Input) string {
	return filepath.Join(workspace, input.RootDir, pathHash(input.AppID), pathHash(input.ChannelKey), input.SessionID, input.AttachmentID, ".attachment-"+pathHash(input.AttachmentID)+".part")
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
	input := Input{
		WorkspaceDir: workspace, RootDir: ".codex-workspace-bot/attachments", AppID: "app-1", ChannelKey: "group:oc-1:app-1", SessionID: "session-1", AttachmentID: "attachment-1",
		Kind: storage.AttachmentFile, SourceMessageID: "om-1", ResourceKey: "resource-key", OriginalName: "report.txt",
	}
	result, err := processor.Materialize(context.Background(), input)
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
	if _, err := os.Stat(temporaryAttachmentPath(workspace, input)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("part file stat error=%v", err)
	}
}

func TestMaterializePublishesSafeOriginalFilename(t *testing.T) {
	workspace := t.TempDir()
	processor := Processor{Downloader: fakeDownloader{body: []byte("ordinary file contents")}, MaxFileBytes: 30_000_000}
	result, err := processor.Materialize(context.Background(), Input{
		WorkspaceDir: workspace, RootDir: ".codex-workspace-bot/attachments", AppID: "app-1", ChannelKey: "group:oc-1:app-1", SessionID: "session-1", AttachmentID: "attachment-1",
		Kind: storage.AttachmentFile, SourceMessageID: "om-1", ResourceKey: "resource-key", OriginalName: "report.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(result.RelativePath); got != "report.txt" {
		t.Fatalf("stored leaf=%q, want report.txt", got)
	}
}

func TestMaterializeSanitizesTraversalOriginalFilename(t *testing.T) {
	workspace := t.TempDir()
	processor := Processor{Downloader: fakeDownloader{body: []byte("ordinary file contents")}, MaxFileBytes: 30_000_000}
	result, err := processor.Materialize(context.Background(), Input{
		WorkspaceDir: workspace, RootDir: ".codex-workspace-bot/attachments", AppID: "app-1", ChannelKey: "group:oc-1:app-1", SessionID: "session-1", AttachmentID: "attachment-1",
		Kind: storage.AttachmentFile, SourceMessageID: "om-1", ResourceKey: "resource-key", OriginalName: "../report.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(result.RelativePath); got != "report.txt" {
		t.Fatalf("stored leaf=%q, want sanitized report.txt", got)
	}
}

func TestMaterializeSeparatesSameNamedAttachmentsByAttachmentID(t *testing.T) {
	workspace := t.TempDir()
	processor := Processor{Downloader: fakeDownloader{body: []byte("ordinary file contents")}, MaxFileBytes: 30_000_000}
	materialize := func(attachmentID string) Result {
		t.Helper()
		result, err := processor.Materialize(context.Background(), Input{
			WorkspaceDir: workspace, RootDir: ".codex-workspace-bot/attachments", AppID: "app-1", ChannelKey: "group:oc-1:app-1", SessionID: "session-1", AttachmentID: attachmentID,
			Kind: storage.AttachmentFile, SourceMessageID: "om-1", ResourceKey: "resource-key", OriginalName: "report.txt",
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	firstID := "00000000-0000-0000-0000-000000000001"
	secondID := "00000000-0000-0000-0000-000000000002"
	first := materialize(firstID)
	second := materialize(secondID)
	if filepath.Dir(first.RelativePath) == filepath.Dir(second.RelativePath) {
		t.Fatalf("same-name attachments share directory: %q", filepath.Dir(first.RelativePath))
	}
	if got := filepath.Base(filepath.Dir(first.RelativePath)); got != firstID {
		t.Fatalf("first attachment directory=%q, want %q", got, firstID)
	}
	if got := filepath.Base(filepath.Dir(second.RelativePath)); got != secondID {
		t.Fatalf("second attachment directory=%q, want %q", got, secondID)
	}
	if filepath.Base(first.RelativePath) != "report.txt" || filepath.Base(second.RelativePath) != "report.txt" {
		t.Fatalf("stored leaves = %q, %q; want report.txt", filepath.Base(first.RelativePath), filepath.Base(second.RelativePath))
	}
}

type blockingDownloader struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (d blockingDownloader) Download(context.Context, string, string, storage.AttachmentKind) (io.ReadCloser, string, error) {
	return &blockingReader{started: d.started, release: d.release}, "source-name", nil
}

type blockingReader struct {
	started chan<- struct{}
	release <-chan struct{}
	wrote   bool
}

func (r *blockingReader) Read(p []byte) (int, error) {
	if r.wrote {
		return 0, io.EOF
	}
	r.wrote = true
	close(r.started)
	<-r.release
	return copy(p, "ordinary file contents"), nil
}

func (r *blockingReader) Close() error { return nil }

func TestMaterializeUsesSafeTemporaryFilename(t *testing.T) {
	workspace := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	processor := Processor{Downloader: blockingDownloader{started: started, release: release}, MaxFileBytes: 30_000_000}
	input := Input{
		WorkspaceDir: workspace, RootDir: ".codex-workspace-bot/attachments", AppID: "app-1", ChannelKey: "group:oc-1:app-1", SessionID: "session-1", AttachmentID: "00000000-0000-0000-0000-000000000002",
		Kind: storage.AttachmentFile, SourceMessageID: "om-1", ResourceKey: "resource-key", OriginalName: "../report.txt",
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := processor.Materialize(context.Background(), input)
		errCh <- err
	}()
	<-started
	part := temporaryAttachmentPath(workspace, input)
	_, statErr := os.Stat(part)
	close(release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if statErr != nil {
		t.Fatalf("safe temporary file stat error=%v", statErr)
	}
}

func TestMaterializeUsesFallbackForDotTraversalName(t *testing.T) {
	workspace := t.TempDir()
	processor := Processor{Downloader: fakeDownloader{body: []byte("ordinary file contents")}, MaxFileBytes: 30_000_000}
	result, err := processor.Materialize(context.Background(), Input{
		WorkspaceDir: workspace, RootDir: ".codex-workspace-bot/attachments", AppID: "app-1", ChannelKey: "group:oc-1:app-1", SessionID: "session-1", AttachmentID: "attachment-1",
		Kind: storage.AttachmentFile, SourceMessageID: "om-1", ResourceKey: "resource-key", OriginalName: "..",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(result.RelativePath); got != "attachment" {
		t.Fatalf("stored leaf=%q, want fallback attachment", got)
	}
}

func TestMaterializeUsesFallbackForEmptyName(t *testing.T) {
	workspace := t.TempDir()
	processor := Processor{Downloader: fakeDownloader{body: []byte("ordinary file contents")}, MaxFileBytes: 30_000_000}
	result, err := processor.Materialize(context.Background(), Input{
		WorkspaceDir: workspace, RootDir: ".codex-workspace-bot/attachments", AppID: "app-1", ChannelKey: "group:oc-1:app-1", SessionID: "session-1", AttachmentID: "attachment-1",
		Kind: storage.AttachmentFile, SourceMessageID: "om-1", ResourceKey: "resource-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(result.RelativePath); got != "attachment" {
		t.Fatalf("stored leaf=%q, want fallback attachment", got)
	}
}

func TestMaterializePreservesBoundaryUnicodeFilenameWithBoundedTemporaryLeaf(t *testing.T) {
	workspace := t.TempDir()
	name := strings.Repeat("界", 83) + ".pdf"
	if len(name) != 253 {
		t.Fatalf("boundary name bytes=%d, want 253", len(name))
	}
	processor := Processor{Downloader: fakeDownloader{body: []byte("ordinary file contents")}, MaxFileBytes: 30_000_000}
	result, err := processor.Materialize(context.Background(), Input{
		WorkspaceDir: workspace, RootDir: ".codex-workspace-bot/attachments", AppID: "app-1", ChannelKey: "group:oc-1:app-1", SessionID: "session-1", AttachmentID: "00000000-0000-0000-0000-000000000002",
		Kind: storage.AttachmentFile, SourceMessageID: "om-1", ResourceKey: "resource-key", OriginalName: name,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(result.RelativePath); got != name {
		t.Fatalf("stored leaf=%q, want original boundary name", got)
	}
}

func TestMaterializeBoundsOverlongUnicodeFilenameAndPreservesExtension(t *testing.T) {
	workspace := t.TempDir()
	processor := Processor{Downloader: fakeDownloader{body: []byte("ordinary file contents")}, MaxFileBytes: 30_000_000}
	result, err := processor.Materialize(context.Background(), Input{
		WorkspaceDir: workspace, RootDir: ".codex-workspace-bot/attachments", AppID: "app-1", ChannelKey: "group:oc-1:app-1", SessionID: "session-1", AttachmentID: "00000000-0000-0000-0000-000000000002",
		Kind: storage.AttachmentFile, SourceMessageID: "om-1", ResourceKey: "resource-key", OriginalName: strings.Repeat("报告", 100) + ".pdf",
	})
	if err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Base(result.RelativePath)
	if len(leaf) > 255 || !utf8.ValidString(leaf) || !strings.HasSuffix(leaf, ".pdf") {
		t.Fatalf("stored leaf=%q bytes=%d", leaf, len(leaf))
	}
	if _, err := os.Stat(filepath.Join(workspace, result.RelativePath)); err != nil {
		t.Fatalf("final file stat error=%v", err)
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
	input := Input{
		WorkspaceDir: workspace, RootDir: ".codex-workspace-bot/attachments", AppID: "app-1", ChannelKey: "group:oc-1:app-1", SessionID: "session-1", AttachmentID: "attachment-1",
		Kind: storage.AttachmentFile, SourceMessageID: "om-1", ResourceKey: "resource-key",
	}
	_, err := processor.Materialize(context.Background(), input)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Materialize() error=%v, want ErrTooLarge", err)
	}
	if _, err := os.Stat(temporaryAttachmentPath(workspace, input)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file stat error=%v", err)
	}
}
