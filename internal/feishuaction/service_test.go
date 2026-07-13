package feishuaction

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/codexapp"
	"github.com/kid0317/codex-workspace-bot/internal/config"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

type terminalLedger struct {
	claimed   int
	started   int
	finalized []storage.ActionResult
}

func (l *terminalLedger) ClaimAction(context.Context, storage.ActionCall) (storage.ActionClaim, error) {
	l.claimed++
	return storage.ActionClaimed, nil
}

func (l *terminalLedger) StartAction(context.Context, string, string, string, string) (bool, error) {
	l.started++
	return true, nil
}

func (l *terminalLedger) CompleteAction(_ context.Context, result storage.ActionResult) error {
	l.finalized = append(l.finalized, result)
	return nil
}

func TestServiceRecordsRejectedInvalidActionAsEncryptedTerminalResult(t *testing.T) {
	protector, err := NewResultProtector([]config.KeyConfig{{Version: 1, Key: bytes.Repeat([]byte{7}, 32)}})
	if err != nil {
		t.Fatal(err)
	}
	ledger := &terminalLedger{}
	service := Service{Clients: map[string]Client{"app-1": &fakeClient{}}, Ledger: ledger, ResultLedger: ledger, Protector: protector}
	result, err := service.Execute(context.Background(), Route{AppID: "app-1", ChannelKey: "group:oc-1:app-1", ChatGroupID: "group-1", Reply: worker.ReplyTarget{ID: "oc-current", Type: "chat_id"}}, codexapp.ToolCall{ThreadID: "thread-1", TurnID: "turn-1", CallID: "call-1", Tool: "feishu.message_send_current_channel", Arguments: []byte(`{}`)})
	if err != nil || result.Success {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if ledger.claimed != 1 || ledger.started != 0 || len(ledger.finalized) != 1 || ledger.finalized[0].State != storage.ActionRejected || len(ledger.finalized[0].ResultEnc) == 0 {
		t.Fatalf("claimed=%d started=%d finalized=%#v", ledger.claimed, ledger.started, ledger.finalized)
	}
}

func TestServiceRecordsUnknownAfterNetworkAttemptWithoutRetry(t *testing.T) {
	protector, err := NewResultProtector([]config.KeyConfig{{Version: 1, Key: bytes.Repeat([]byte{8}, 32)}})
	if err != nil {
		t.Fatal(err)
	}
	ledger := &terminalLedger{}
	client := &fakeClient{textErr: errors.New("network timeout")}
	service := Service{Clients: map[string]Client{"app-1": client}, Ledger: ledger, ResultLedger: ledger, Protector: protector}
	arguments, _ := json.Marshal(map[string]string{"text": "summary"})
	result, err := service.Execute(context.Background(), Route{AppID: "app-1", ChannelKey: "group:oc-1:app-1", ChatGroupID: "group-1", Reply: worker.ReplyTarget{ID: "oc-current", Type: "chat_id"}}, codexapp.ToolCall{ThreadID: "thread-1", TurnID: "turn-1", CallID: "call-1", Tool: "feishu.message_send_current_channel", Arguments: arguments})
	if err != nil || result.Success || ledger.started != 1 || len(ledger.finalized) != 1 || ledger.finalized[0].State != storage.ActionUnknown || client.calls != 1 {
		t.Fatalf("result=%#v err=%v started=%d finalized=%#v calls=%d", result, err, ledger.started, ledger.finalized, client.calls)
	}
}

func TestServiceCancelsClaimedActionBeforeStartingExternalCall(t *testing.T) {
	protector, err := NewResultProtector([]config.KeyConfig{{Version: 1, Key: bytes.Repeat([]byte{9}, 32)}})
	if err != nil {
		t.Fatal(err)
	}
	ledger := &terminalLedger{}
	client := &fakeClient{}
	service := Service{Clients: map[string]Client{"app-1": client}, Ledger: ledger, ResultLedger: ledger, Protector: protector}
	arguments, _ := json.Marshal(map[string]string{"text": "summary"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := service.Execute(ctx, Route{AppID: "app-1", ChannelKey: "group:oc-1:app-1", ChatGroupID: "group-1", Reply: worker.ReplyTarget{ID: "oc-current", Type: "chat_id"}}, codexapp.ToolCall{ThreadID: "thread-1", TurnID: "turn-1", CallID: "call-1", Tool: "feishu.message_send_current_channel", Arguments: arguments})
	if err != nil || result.Success || ledger.claimed != 1 || ledger.started != 0 || len(ledger.finalized) != 1 || ledger.finalized[0].State != storage.ActionCancelledBeforeSend || client.calls != 0 {
		t.Fatalf("result=%#v err=%v claimed=%d started=%d finalized=%#v calls=%d", result, err, ledger.claimed, ledger.started, ledger.finalized, client.calls)
	}
}

func TestOpenOrdinaryFileAllowsAnyLocalRegularPathAndFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	payload := filepath.Join(dir, "outside-workspace.txt")
	if err := os.WriteFile(payload, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "alias")
	if err := os.Symlink(payload, link); err != nil {
		t.Fatal(err)
	}
	file, name, size, err := OpenOrdinaryFile(link, "", 30_000_000)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if name != "outside-workspace.txt" || size != int64(len("payload")) {
		t.Fatalf("name=%q size=%d", name, size)
	}
}

type conflictLedger struct{}

func (conflictLedger) ClaimAction(context.Context, storage.ActionCall) (storage.ActionClaim, error) {
	return storage.ActionConflict, nil
}

func (conflictLedger) StartAction(context.Context, string, string, string, string) (bool, error) {
	return false, nil
}

func TestServiceDoesNotRepeatConflictingActionCall(t *testing.T) {
	client := &fakeClient{}
	service := Service{Clients: map[string]Client{"app-1": client}, Ledger: conflictLedger{}}
	arguments, _ := json.Marshal(map[string]string{"text": "summary"})
	result, err := service.Execute(context.Background(), Route{AppID: "app-1", ChannelKey: "group:oc-1:app-1", ChatGroupID: "group-1", Reply: worker.ReplyTarget{ID: "oc-current", Type: "chat_id"}}, codexapp.ToolCall{ThreadID: "thread-1", TurnID: "turn-1", CallID: "call-1", Tool: "feishu.message_send_current_channel", Arguments: arguments})
	if err != nil || result.Success || client.text != "" {
		t.Fatalf("result=%#v err=%v client=%#v", result, err, client)
	}
}

func TestReadMarkdownFileAllowsAnyLocalRegularPathAndRejectsInvalidUTF8(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")
	if err := os.WriteFile(path, []byte("# Report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents, err := ReadMarkdownFile(path, 1_000)
	if err != nil || string(contents) != "# Report\n" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if _, err := ReadMarkdownFile(dir, 1_000); !errors.Is(err, ErrInvalidMarkdown) {
		t.Fatalf("directory error=%v", err)
	}
	invalid := filepath.Join(dir, "invalid.md")
	if err := os.WriteFile(invalid, []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMarkdownFile(invalid, 1_000); !errors.Is(err, ErrInvalidMarkdown) {
		t.Fatalf("invalid utf8 error=%v", err)
	}
}

type fakeClient struct {
	target  worker.ReplyTarget
	text    string
	files   int
	docs    int
	docID   string
	docText string
	textErr error
	calls   int
}

func (f *fakeClient) SendCurrentText(_ context.Context, target worker.ReplyTarget, text string) (string, error) {
	f.calls++
	f.target, f.text = target, text
	if f.textErr != nil {
		return "", f.textErr
	}
	return "om-text", nil
}
func (f *fakeClient) UploadAndSend(_ context.Context, target worker.ReplyTarget, _ *os.File, _ string) (string, string, error) {
	f.target, f.files = target, f.files+1
	return "file-key", "om-file", nil
}
func (f *fakeClient) CreateDocumentAndAnnounce(_ context.Context, target worker.ReplyTarget, _ string, markdown []byte) (worker.DocumentOutcome, error) {
	f.target, f.docs = target, f.docs+1
	if len(markdown) == 0 {
		return worker.DocumentOutcome{}, errors.New("missing markdown")
	}
	return worker.DocumentOutcome{URL: "https://example.test/docx/doc-1", ContentWritten: true, AnnouncementOutcome: "sent"}, nil
}

func (f *fakeClient) ReadDocument(_ context.Context, documentID string) (string, error) {
	f.docID = documentID
	return f.docText, nil
}

func TestServiceExecutesMessageOnlyOnBoundCurrentChannel(t *testing.T) {
	client := &fakeClient{}
	service := Service{Clients: map[string]Client{"app-1": client}, MaxFileBytes: 30_000_000}
	arguments, _ := json.Marshal(map[string]string{"text": "summary"})
	result, err := service.Execute(context.Background(), Route{AppID: "app-1", Reply: worker.ReplyTarget{ID: "oc-current", Type: "chat_id"}}, codexapp.ToolCall{Tool: "feishu.message_send_current_channel", Arguments: arguments})
	if err != nil || !result.Success || client.target.ID != "oc-current" || client.text != "summary" {
		t.Fatalf("result=%#v err=%v client=%#v", result, err, client)
	}
}

func TestServiceFileActionAcceptsArbitraryLocalRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{}
	service := Service{Clients: map[string]Client{"app-1": client}, MaxFileBytes: 30_000_000}
	arguments, _ := json.Marshal(map[string]string{"file_path": path})
	result, err := service.Execute(context.Background(), Route{AppID: "app-1", Reply: worker.ReplyTarget{ID: "ou-current", Type: "open_id"}}, codexapp.ToolCall{Tool: "feishu.file_upload_and_send_current_channel", Arguments: arguments})
	if err != nil || !result.Success || client.files != 1 || client.target.ID != "ou-current" {
		t.Fatalf("result=%#v err=%v client=%#v", result, err, client)
	}
}

func TestServiceCreatesDocumentFromAnyLocalMarkdownAndAnnouncesIt(t *testing.T) {
	dir := t.TempDir()
	markdownPath := filepath.Join(dir, "result.md")
	if err := os.WriteFile(markdownPath, []byte("# Result\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{}
	service := Service{Clients: map[string]Client{"app-1": client}, MaxMarkdownBytes: 1_000}
	arguments, _ := json.Marshal(map[string]string{"markdown_ref": markdownPath, "title": "Result"})
	result, err := service.Execute(context.Background(), Route{AppID: "app-1", Reply: worker.ReplyTarget{ID: "oc-current", Type: "chat_id"}}, codexapp.ToolCall{Tool: "feishu.doc_create_and_announce", Arguments: arguments})
	if err != nil || !result.Success || client.docs != 1 || client.target.ID != "oc-current" || len(result.ContentItems) != 1 || !strings.Contains(result.ContentItems[0].Text, `"content_written":true`) || !strings.Contains(result.ContentItems[0].Text, `"announcement_outcome":"sent"`) {
		t.Fatalf("result=%#v err=%v client=%#v", result, err, client)
	}
}

func TestServiceCreatesDocumentFromWorkspaceRelativeMarkdownRef(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "result.md"), []byte("# Result\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{}
	service := Service{Clients: map[string]Client{"app-1": client}, MaxMarkdownBytes: 1_000}
	arguments, _ := json.Marshal(map[string]string{"markdown_ref": "result.md", "title": "Result"})
	result, err := service.Execute(context.Background(), Route{AppID: "app-1", WorkspaceDir: workspace, Reply: worker.ReplyTarget{ID: "oc-current", Type: "chat_id"}}, codexapp.ToolCall{Tool: "feishu.doc_create_and_announce", Arguments: arguments})
	if err != nil || !result.Success || client.docs != 1 {
		t.Fatalf("result=%#v err=%v docs=%d", result, err, client.docs)
	}
}

func TestServiceAcceptsUnqualifiedDynamicDocumentToolName(t *testing.T) {
	dir := t.TempDir()
	markdownPath := filepath.Join(dir, "result.md")
	if err := os.WriteFile(markdownPath, []byte("# Result\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{}
	service := Service{Clients: map[string]Client{"app-1": client}, MaxMarkdownBytes: 1_000}
	arguments, _ := json.Marshal(map[string]string{"markdown_ref": markdownPath, "title": "Result"})
	result, err := service.Execute(context.Background(), Route{AppID: "app-1", Reply: worker.ReplyTarget{ID: "oc-current", Type: "chat_id"}}, codexapp.ToolCall{Tool: "doc_create_and_announce", Arguments: arguments})
	if err != nil || !result.Success || client.docs != 1 {
		t.Fatalf("result=%#v err=%v docs=%d", result, err, client.docs)
	}
}

func TestServiceReadsFeishuDocumentWithUnqualifiedDynamicToolName(t *testing.T) {
	client := &fakeClient{docText: "document body"}
	service := Service{Clients: map[string]Client{"app-1": client}}
	arguments, _ := json.Marshal(map[string]string{"document_url": "https://example.feishu.cn/docx/EYD9dU6nRo1qG9xVLpmcsnLunye"})
	result, err := service.Execute(context.Background(), Route{AppID: "app-1", Reply: worker.ReplyTarget{ID: "oc-current", Type: "chat_id"}}, codexapp.ToolCall{Tool: "doc_read", Arguments: arguments})
	if err != nil || !result.Success || len(result.ContentItems) != 1 || result.ContentItems[0].Text != "document body" || client.docID != "EYD9dU6nRo1qG9xVLpmcsnLunye" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestServiceRejectsNonFeishuDocumentURLWithoutReading(t *testing.T) {
	client := &fakeClient{docText: "must not be read"}
	service := Service{Clients: map[string]Client{"app-1": client}}
	arguments, _ := json.Marshal(map[string]string{"document_url": "https://example.com/docx/EYD9dU6nRo1qG9xVLpmcsnLunye"})
	result, err := service.Execute(context.Background(), Route{AppID: "app-1", Reply: worker.ReplyTarget{ID: "oc-current", Type: "chat_id"}}, codexapp.ToolCall{Tool: "doc_read", Arguments: arguments})
	if err != nil || result.Success || client.docID != "" {
		t.Fatalf("result=%#v err=%v document_id=%q", result, err, client.docID)
	}
}

func TestServiceRejectsDocumentWithEmptyTitleBeforeCallingFeishu(t *testing.T) {
	dir := t.TempDir()
	markdownPath := filepath.Join(dir, "result.md")
	if err := os.WriteFile(markdownPath, []byte("# Result\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{}
	service := Service{Clients: map[string]Client{"app-1": client}, MaxMarkdownBytes: 1_000}
	arguments, _ := json.Marshal(map[string]string{"markdown_ref": markdownPath, "title": ""})
	result, err := service.Execute(context.Background(), Route{AppID: "app-1", Reply: worker.ReplyTarget{ID: "oc-current", Type: "chat_id"}}, codexapp.ToolCall{Tool: "feishu.doc_create_and_announce", Arguments: arguments})
	if err != nil || result.Success || client.docs != 0 {
		t.Fatalf("result=%#v err=%v docs=%d", result, err, client.docs)
	}
}

func TestOpenOrdinaryFileRejectsDirectoryEmptyAndOverLimit(t *testing.T) {
	dir := t.TempDir()
	if _, _, _, err := OpenOrdinaryFile(dir, "", 30_000_000); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("directory error=%v", err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := OpenOrdinaryFile(empty, "", 30_000_000); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("empty error=%v", err)
	}
	large := filepath.Join(dir, "large")
	if err := os.WriteFile(large, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := OpenOrdinaryFile(large, "", 4); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("large error=%v", err)
	}
}
