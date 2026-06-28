package session_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/db"
	"github.com/kid0317/codex-workspace-bot/internal/engine"
	"github.com/kid0317/codex-workspace-bot/internal/feishu"
	"github.com/kid0317/codex-workspace-bot/internal/guardrail"
	"github.com/kid0317/codex-workspace-bot/internal/mockengine"
	"github.com/kid0317/codex-workspace-bot/internal/model"
	"github.com/kid0317/codex-workspace-bot/internal/observability"
	"github.com/kid0317/codex-workspace-bot/internal/session"
)

func TestWorkModeDispatchReusesThreadAndNewResetsSession(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work"})
	msg := incoming("m1", "hello")
	if err := mgr.Dispatch(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Dispatch(context.Background(), incoming("m2", "again")); err != nil {
		t.Fatal(err)
	}
	sessions, err := store.Sessions().ByChannel(msg.ChannelKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].EngineThreadID == "" {
		t.Fatalf("sessions after reuse = %#v", sessions)
	}
	firstThread := sessions[0].EngineThreadID
	if err := mgr.Dispatch(context.Background(), incoming("m3", "/new")); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Dispatch(context.Background(), incoming("m4", "fresh")); err != nil {
		t.Fatal(err)
	}
	sessions, _ = store.Sessions().ByChannel(msg.ChannelKey)
	if len(sessions) != 2 {
		t.Fatalf("session count = %d, want 2: %#v", len(sessions), sessions)
	}
	var active model.Session
	for _, s := range sessions {
		if s.Status == model.SessionActive {
			active = s
		}
	}
	if active.EngineThreadID == "" || active.EngineThreadID == firstThread {
		t.Fatalf("active thread = %q, first = %q", active.EngineThreadID, firstThread)
	}
	if !sender.HasCallSequence("SendThinking", "UpdateCard", "SendThinking", "UpdateCard", "SendText", "SendThinking", "UpdateCard") {
		t.Fatalf("sender calls = %#v", sender.Calls())
	}
}

func TestCompanionModeUsesFreshThreadAndDirectText(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "companion"})
	if err := mgr.Dispatch(context.Background(), incoming("m1", "one")); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Dispatch(context.Background(), incoming("m2", "two")); err != nil {
		t.Fatal(err)
	}
	turns, err := store.Turns().All()
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 || turns[0].EngineThreadID == turns[1].EngineThreadID {
		t.Fatalf("turns = %#v", turns)
	}
	if !sender.HasOnly("SendText") {
		t.Fatalf("sender calls = %#v", sender.Calls())
	}
}

func TestAttachmentPendingSurvivesManagerRestartAndIsConsumedByText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")
	root := t.TempDir()
	store, _ := db.Open(path)
	sender := feishu.NewMockSender()
	engine := &recordingEngine{}
	tempDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := session.NewManager(store, engine, sender, session.Options{WorkspaceMode: "work", WorkspaceDir: root, AttachmentTempDir: tempDir})
	attMsg := incoming("m1", "")
	tempFile := filepath.Join(tempDir, "report.txt")
	if err := os.WriteFile(tempFile, []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}
	attMsg.Attachments = []feishu.Attachment{{ID: "a1", OriginalName: "report.txt", TempPath: tempFile}}
	if err := mgr.Dispatch(context.Background(), attMsg); err != nil {
		t.Fatal(err)
	}
	pending, err := store.Attachments().ByChannelState(attMsg.ChannelKey, model.AttachmentPending)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending attachments = %#v err=%v", pending, err)
	}
	store, _ = db.Open(path)
	mgr = session.NewManager(store, engine, sender, session.Options{WorkspaceMode: "work", WorkspaceDir: root, AttachmentTempDir: tempDir})
	if err := mgr.Dispatch(context.Background(), incoming("m2", "use attachment")); err != nil {
		t.Fatal(err)
	}
	consumed, err := store.Attachments().ByChannelState(attMsg.ChannelKey, model.AttachmentConsumed)
	if err != nil || len(consumed) != 1 {
		t.Fatalf("consumed attachments = %#v err=%v", consumed, err)
	}
	if !strings.Contains(engine.prompt, "report.txt") || !strings.Contains(engine.prompt, "attachments") {
		t.Fatalf("engine prompt did not include attachment reference: %q", engine.prompt)
	}
	if consumed[0].SessionPath == "" {
		t.Fatalf("consumed attachment missing session path: %#v", consumed[0])
	}
	if _, err := os.Stat(consumed[0].SessionPath); err != nil {
		t.Fatalf("session attachment file missing: %v", err)
	}
}

func TestAttachmentConsumeRejectsUnsafeTempPathAndKeepsUniqueNames(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	sender := feishu.NewMockSender()
	tempDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work", WorkspaceDir: root, AttachmentTempDir: tempDir})
	msg := incoming("m1", "")
	safe1 := filepath.Join(tempDir, "report.txt")
	safe2 := filepath.Join(tempDir, "report-copy.txt")
	if err := os.WriteFile(safe1, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(safe2, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg.Attachments = []feishu.Attachment{
		{ID: "a1", OriginalName: "../report.txt", TempPath: safe1},
		{ID: "a2", OriginalName: "../report.txt", TempPath: safe2},
	}
	if err := mgr.Dispatch(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Dispatch(context.Background(), incoming("m2", "use")); err != nil {
		t.Fatal(err)
	}
	consumed, _ := store.Attachments().ByChannelState(msg.ChannelKey, model.AttachmentConsumed)
	if len(consumed) != 2 || consumed[0].SessionPath == consumed[1].SessionPath {
		t.Fatalf("expected unique consumed attachment paths: %#v", consumed)
	}

	unsafeMsg := incoming("m3", "")
	unsafeMsg.Attachments = []feishu.Attachment{{ID: "a3", OriginalName: "bad.txt", TempPath: filepath.Join(root, "..", "outside.txt")}}
	if err := mgr.Dispatch(context.Background(), unsafeMsg); err == nil {
		t.Fatal("unsafe temp path should be rejected")
	}
}

func TestAttachmentConsumeRejectsAbsolutePathOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work", WorkspaceDir: root})

	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg := incoming("m1", "")
	msg.Attachments = []feishu.Attachment{{ID: "a1", OriginalName: "secret.txt", TempPath: outside}}
	if err := mgr.Dispatch(context.Background(), msg); err == nil {
		t.Fatal("absolute temp path outside workspace should be rejected")
	}
}

func TestAttachmentConsumeRequiresConfiguredTempRoot(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work", WorkspaceDir: root})

	secret := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg := incoming("m1", "")
	msg.Attachments = []feishu.Attachment{{ID: "a1", OriginalName: "secret.txt", TempPath: secret}}
	if err := mgr.Dispatch(context.Background(), msg); err == nil {
		t.Fatal("attachment temp path under workspace should be rejected when AttachmentTempDir is unset")
	}
}

func TestAttachmentConsumeRejectsRelativeTempPath(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work", WorkspaceDir: root})

	msg := incoming("m1", "")
	msg.Attachments = []feishu.Attachment{{ID: "a1", OriginalName: "session.go", TempPath: "session.go"}}
	if err := mgr.Dispatch(context.Background(), msg); err == nil {
		t.Fatal("relative temp path should be rejected")
	}
}

func TestAttachmentConsumeRejectsIntermediateSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	tempDir := filepath.Join(root, "tmp")
	outside := t.TempDir()
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tempDir, "link")); err != nil {
		t.Fatal(err)
	}
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work", WorkspaceDir: root, AttachmentTempDir: tempDir})

	msg := incoming("m1", "")
	msg.Attachments = []feishu.Attachment{{ID: "a1", OriginalName: "secret.txt", TempPath: filepath.Join(tempDir, "link", "secret.txt")}}
	if err := mgr.Dispatch(context.Background(), msg); err == nil {
		t.Fatal("intermediate symlink escape should be rejected")
	}
}

func TestAttachmentPendingLimitRejectsTooManyAttachments(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work", WorkspaceDir: root, MaxPendingAttachments: 1})

	msg := incoming("m1", "")
	msg.Attachments = []feishu.Attachment{
		{ID: "a1", OriginalName: "one.txt"},
		{ID: "a2", OriginalName: "two.txt"},
	}
	if err := mgr.Dispatch(context.Background(), msg); err == nil {
		t.Fatal("too many pending attachments should be rejected")
	}
}

func TestDuplicateMessageIDIsIdempotent(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work"})
	msg := incoming("dup-message", "hello")
	if err := mgr.Dispatch(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Dispatch(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	messages, err := store.Messages().All()
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("duplicate dispatch wrote %d messages, want only original user+assistant", len(messages))
	}
	if messages[1].Content != "hello" {
		t.Fatalf("assistant content = %q, want single hello", messages[1].Content)
	}
	if !sender.HasCallSequence("SendThinking", "UpdateCard") {
		t.Fatalf("duplicate dispatch sender calls = %#v", sender.Calls())
	}
}

func TestDuplicateReceiptExpiresAfterConfiguredTTL(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err := store.EventReceipts().Save("expired-message", "demo"); err != nil {
		t.Fatal(err)
	}
	if err := store.EventReceipts().SetCreatedAtForTest("expired-message", time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work", DuplicateMessageTTL: time.Hour})
	if err := mgr.Dispatch(context.Background(), incoming("expired-message", "after ttl")); err != nil {
		t.Fatal(err)
	}
	messages, err := store.Messages().All()
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("expired receipt should allow processing, got %#v", messages)
	}
}

func TestDuplicateAttachmentOnlyAndNewEventsAreIdempotent(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work"})

	att := incoming("dup-attachment", "")
	att.Attachments = []feishu.Attachment{{ID: "a1", OriginalName: "a.txt"}}
	if err := mgr.Dispatch(context.Background(), att); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Dispatch(context.Background(), att); err != nil {
		t.Fatal(err)
	}
	pending, _ := store.Attachments().ByChannelState(att.ChannelKey, model.AttachmentPending)
	if len(pending) != 1 {
		t.Fatalf("duplicate attachment-only dispatch wrote %#v", pending)
	}

	newMsg := incoming("dup-new", "/new")
	if err := mgr.Dispatch(context.Background(), incoming("before-new", "hello")); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Dispatch(context.Background(), newMsg); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Dispatch(context.Background(), newMsg); err != nil {
		t.Fatal(err)
	}
	var newReceipts int
	for _, call := range sender.Calls() {
		if call.Method == "SendText" && strings.Contains(call.Text, "新对话") {
			newReceipts++
		}
	}
	if newReceipts != 1 {
		t.Fatalf("duplicate /new sent %d receipts, calls=%#v", newReceipts, sender.Calls())
	}
}

func TestCleanupExpiresPendingAttachments(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work"})
	msg := incoming("m1", "")
	msg.Attachments = []feishu.Attachment{{ID: "a1", OriginalName: "old.txt"}}
	if err := mgr.Dispatch(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := session.CleanupExpiredAttachments(store, msg.ChannelKey, 0); err != nil {
		t.Fatal(err)
	}
	expired, err := store.Attachments().ByChannelState(msg.ChannelKey, model.AttachmentExpired)
	if err != nil || len(expired) != 1 {
		t.Fatalf("expired attachments = %#v err=%v", expired, err)
	}
}

func TestCleanupExpiredAttachmentsRespectsTTLAndDeletesFiles(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	oldFile := filepath.Join(root, "old.tmp")
	newFile := filepath.Join(root, "new.tmp")
	if err := os.WriteFile(oldFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	recent := time.Now()
	if err := store.Attachments().Save(model.Attachment{ID: "old", AppID: "demo", ChannelKey: "p2p:oc_p2p:demo", State: model.AttachmentPending, TempPath: oldFile, CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := store.Attachments().Save(model.Attachment{ID: "new", AppID: "demo", ChannelKey: "p2p:oc_p2p:demo", State: model.AttachmentPending, TempPath: newFile, CreatedAt: recent}); err != nil {
		t.Fatal(err)
	}
	if err := session.CleanupExpiredAttachments(store, "p2p:oc_p2p:demo", 3600); err != nil {
		t.Fatal(err)
	}
	expired, _ := store.Attachments().ByChannelState("p2p:oc_p2p:demo", model.AttachmentExpired)
	if len(expired) != 1 || expired[0].ID != "old" {
		t.Fatalf("expired attachments = %#v", expired)
	}
	pending, _ := store.Attachments().ByChannelState("p2p:oc_p2p:demo", model.AttachmentPending)
	if len(pending) != 1 || pending[0].ID != "new" {
		t.Fatalf("pending attachments = %#v", pending)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatalf("old temp file still exists or unexpected error: %v", err)
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Fatalf("new temp file should remain: %v", err)
	}
}

func TestCleanupExpiredAttachmentsDoesNotDeleteOutsideRoots(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Attachments().Save(model.Attachment{ID: "outside", AppID: "demo", ChannelKey: "p2p:oc_p2p:demo", State: model.AttachmentPending, TempPath: outside, CreatedAt: time.Now().Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := session.CleanupExpiredAttachments(store, "p2p:oc_p2p:demo", 3600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("cleanup should not delete outside path: %v", err)
	}
}

func TestExpiredPendingAttachmentIsNotConsumedIntoPrompt(t *testing.T) {
	root := t.TempDir()
	tempDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(tempDir, "old.txt")
	if err := os.WriteFile(oldFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	if err := store.Attachments().Save(model.Attachment{ID: "old", AppID: "demo", ChannelKey: "p2p:oc_p2p:demo", State: model.AttachmentPending, OriginalName: "old.txt", TempPath: oldFile, CreatedAt: time.Now().Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	engine := &recordingEngine{}
	mgr := session.NewManager(store, engine, feishu.NewMockSender(), session.Options{WorkspaceMode: "work", WorkspaceDir: root, AttachmentTempDir: tempDir, PendingAttachmentTTL: time.Hour})
	if err := mgr.Dispatch(context.Background(), incoming("m2", "use current prompt")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(engine.prompt, "old.txt") {
		t.Fatalf("expired attachment was consumed into prompt: %q", engine.prompt)
	}
	expired, _ := store.Attachments().ByChannelState("p2p:oc_p2p:demo", model.AttachmentExpired)
	if len(expired) != 1 {
		t.Fatalf("expired attachments = %#v", expired)
	}
}

func TestDispatchWritesSessionContextAndInjectsRouting(t *testing.T) {
	root := t.TempDir()
	store, _ := db.Open(filepath.Join(root, "bot.db"))
	sender := feishu.NewMockSender()
	engine := &recordingEngine{}
	mgr := session.NewManager(store, engine, sender, session.Options{WorkspaceMode: "work", WorkspaceDir: root})
	if err := mgr.Dispatch(context.Background(), incoming("m1", "hello")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(engine.prompt, "<system_routing>") || !strings.Contains(engine.prompt, "hello") {
		t.Fatalf("engine prompt missing routing: %q", engine.prompt)
	}
	matches, err := filepath.Glob(filepath.Join(root, "sessions", "*", "SESSION_CONTEXT.md"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("context files = %#v err=%v", matches, err)
	}
}

func TestDispatchGuardrailRejectsBeforeEngine(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	sender := feishu.NewMockSender()
	engine := &recordingEngine{}
	mgr := session.NewManager(store, engine, sender, session.Options{
		WorkspaceMode: "work",
		Guardrail:     guardrail.New(guardrail.Config{MaxMessageBytes: 3}),
	})
	if err := mgr.Dispatch(context.Background(), incoming("m1", "1234")); err == nil {
		t.Fatal("Dispatch should reject over-limit input")
	}
	if engine.called {
		t.Fatal("engine was called after guardrail rejection")
	}
	messages, _ := store.Messages().All()
	if len(messages) != 0 {
		t.Fatalf("guardrail rejection wrote messages: %#v", messages)
	}
}

func TestChannelWorkerSerializesSameChannelAndRejectsOverflow(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	sender := feishu.NewMockSender()
	engine := newBlockingEngine()
	emitter := &recordingEmitter{}
	mgr := session.NewManager(store, engine, sender, session.Options{
		WorkspaceMode:     "work",
		QueueSize:         1,
		WorkerIdleTimeout: time.Second,
		Emitter:           emitter,
	})
	defer mgr.Close(context.Background())

	errs := make(chan error, 3)
	go func() { errs <- mgr.Dispatch(context.Background(), incoming("m1", "first")) }()
	engine.waitEntered(t, 1)
	go func() { errs <- mgr.Dispatch(context.Background(), incoming("m2", "second")) }()
	time.Sleep(50 * time.Millisecond)

	overflowCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := mgr.Dispatch(overflowCtx, incoming("m3", "third")); err == nil {
		t.Fatal("overflow dispatch should return busy error")
	}
	engine.releaseOne()
	engine.waitEntered(t, 2)
	engine.releaseOne()
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("queued dispatch failed: %v", err)
		}
	}

	if got := engine.prompts(); strings.Join(got, ",") != "first,second" {
		t.Fatalf("engine prompts = %#v", got)
	}
	turns, _ := store.Turns().All()
	var rejected bool
	for _, turn := range turns {
		if turn.Status == "rejected" && turn.ErrorKind == "queue_overflow" {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("overflow did not persist rejected turn: %#v", turns)
	}
	var busyText bool
	for _, call := range sender.Calls() {
		if call.Method == "SendText" && strings.Contains(call.Text, "稍后") {
			busyText = true
		}
	}
	if !busyText {
		t.Fatalf("overflow did not send busy response: %#v", sender.Calls())
	}
	var rejectedEvent bool
	for _, ev := range emitter.events {
		if ev.EventType == observability.EventDispatchRejected && ev.ErrorKind == "queue_overflow" {
			rejectedEvent = true
		}
	}
	if !rejectedEvent {
		t.Fatalf("overflow did not emit dispatch_rejected: %#v", emitter.events)
	}
}

func TestDispatchEnforcesOutputAndEventGuardrails(t *testing.T) {
	tests := []struct {
		name      string
		events    []engine.TurnEvent
		guardrail guardrail.Config
		errorKind string
	}{
		{
			name: "output",
			events: []engine.TurnEvent{
				{Type: engine.EventTurnStarted, ThreadID: "thread-1"},
				{Type: engine.EventDelta, ThreadID: "thread-1", Text: "too long"},
				{Type: engine.EventCompleted, ThreadID: "thread-1"},
			},
			guardrail: guardrail.Config{MaxOutputBytes: 3},
			errorKind: "output_limit",
		},
		{
			name: "events",
			events: []engine.TurnEvent{
				{Type: engine.EventTurnStarted, ThreadID: "thread-1"},
				{Type: engine.EventDelta, ThreadID: "thread-1", Text: "a"},
				{Type: engine.EventDelta, ThreadID: "thread-1", Text: "b"},
				{Type: engine.EventCompleted, ThreadID: "thread-1"},
			},
			guardrail: guardrail.Config{MaxEventsPerTurn: 2},
			errorKind: "event_limit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
			sender := feishu.NewMockSender()
			mgr := session.NewManager(store, &eventEngine{events: tt.events}, sender, session.Options{
				WorkspaceMode: "work",
				Guardrail:     guardrail.New(tt.guardrail),
			})
			if err := mgr.Dispatch(context.Background(), incoming("m1", "hello")); err == nil {
				t.Fatal("Dispatch should reject guarded engine output")
			}
			turns, _ := store.Turns().All()
			if len(turns) != 1 || turns[0].Status != "failed" || turns[0].ErrorKind != tt.errorKind {
				t.Fatalf("turns = %#v", turns)
			}
		})
	}
}

func TestDispatchPersistsApprovalRequestFromEngineEvent(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, &eventEngine{events: []engine.TurnEvent{
		{Type: engine.EventTurnStarted, ThreadID: "thread-1"},
		{Type: engine.EventApprovalRequested, ThreadID: "thread-1", ApprovalID: "approval-1", ApprovalJSON: `{"tool":"write"}`},
		{Type: engine.EventDelta, ThreadID: "thread-1", Text: "waiting"},
		{Type: engine.EventCompleted, ThreadID: "thread-1"},
	}}, sender, session.Options{WorkspaceMode: "work"})
	if err := mgr.Dispatch(context.Background(), incoming("m1", "needs approval")); err == nil {
		t.Fatal("pending approval should stop dispatch before output is sent")
	}
	req, err := store.Approvals().ByID("approval-1")
	if err != nil {
		t.Fatal(err)
	}
	if req.AppID != "demo" || req.Status != "pending_user" || req.RequestJSON == "" {
		t.Fatalf("approval request = %#v", req)
	}
	messages, _ := store.Messages().All()
	if len(messages) != 1 || messages[0].Role != model.MessageRoleUser {
		t.Fatalf("approval path should only persist user message before approval: %#v", messages)
	}
	if len(sender.Calls()) != 1 || sender.Calls()[0].Method != "SendThinking" {
		t.Fatalf("approval path should not send final assistant output: %#v", sender.Calls())
	}
}

func TestApprovalRequestTerminalFailureStillStaysPending(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, &eventEngine{events: []engine.TurnEvent{
		{Type: engine.EventTurnStarted, ThreadID: "thread-1"},
		{Type: engine.EventApprovalRequested, ThreadID: "thread-1", ApprovalID: "approval-1", ApprovalJSON: `{"tool":"write"}`},
		{Type: engine.EventFailed, ThreadID: "thread-1", Error: "approval required"},
	}}, sender, session.Options{WorkspaceMode: "work"})
	if err := mgr.Dispatch(context.Background(), incoming("m1", "needs approval")); err == nil {
		t.Fatal("pending approval should stop dispatch")
	}
	turns, _ := store.Turns().All()
	if len(turns) != 1 || turns[0].Status != "pending_approval" {
		t.Fatalf("turns = %#v", turns)
	}
	if len(sender.Calls()) != 1 || sender.Calls()[0].Method != "SendThinking" {
		t.Fatalf("approval terminal failure should not send final output: %#v", sender.Calls())
	}
}

func TestExpiredApprovalIsResolvedBeforeNextDispatch(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work"})
	if err := store.Approvals().Save(model.ApprovalRequest{
		ID: "approval-old", AppID: "demo", ChannelKey: "p2p:oc_p2p:demo", Status: "pending_user", ExpiresAt: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Dispatch(context.Background(), incoming("m1", "after approval")); err != nil {
		t.Fatal(err)
	}
	got, err := store.Approvals().ByID("approval-old")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "expired" || got.ResolvedAt == nil {
		t.Fatalf("expired approval = %#v", got)
	}
}

func TestEngineFailurePreservesErrorKindAndUpdatesWorkCard(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	sender := feishu.NewMockSender()
	mgr := session.NewManager(store, &eventEngine{events: []engine.TurnEvent{
		{Type: engine.EventTurnStarted, ThreadID: "thread-1"},
		{Type: engine.EventFailed, ThreadID: "thread-1", Error: "engine_failed"},
	}}, sender, session.Options{WorkspaceMode: "work"})
	if err := mgr.Dispatch(context.Background(), incoming("m1", "fail")); err == nil {
		t.Fatal("engine failure should return error")
	}
	turns, _ := store.Turns().All()
	if len(turns) != 1 || turns[0].Status != string(engine.EventFailed) || turns[0].ErrorKind != "engine_failed" {
		t.Fatalf("turns = %#v", turns)
	}
	if !sender.HasCallSequence("SendThinking", "UpdateCard") {
		t.Fatalf("failure should update thinking card, calls=%#v", sender.Calls())
	}
}

func TestSendFailureFallbackAndCompanionSegmentContinuation(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	workSender := feishu.NewMockSender()
	workSender.FailNext("SendThinking", errors.New("card unavailable"))
	mgr := session.NewManager(store, &eventEngine{events: []engine.TurnEvent{
		{Type: engine.EventTurnStarted, ThreadID: "thread-1"},
		{Type: engine.EventDelta, ThreadID: "thread-1", Text: "ok"},
		{Type: engine.EventCompleted, ThreadID: "thread-1"},
	}}, workSender, session.Options{WorkspaceMode: "work"})
	if err := mgr.Dispatch(context.Background(), incoming("m1", "hello")); err != nil {
		t.Fatal(err)
	}
	if !workSender.HasCallSequence("SendThinking", "SendText") {
		t.Fatalf("work fallback calls = %#v", workSender.Calls())
	}

	companionSender := feishu.NewMockSender()
	companionSender.FailNext("SendText", errors.New("segment failed"))
	companion := session.NewManager(store, &eventEngine{events: []engine.TurnEvent{
		{Type: engine.EventTurnStarted, ThreadID: "thread-2"},
		{Type: engine.EventDelta, ThreadID: "thread-2", Text: "one[[SEND]]two"},
		{Type: engine.EventCompleted, ThreadID: "thread-2"},
	}}, companionSender, session.Options{WorkspaceMode: "companion"})
	if err := companion.Dispatch(context.Background(), incoming("m2", "segment")); err != nil {
		t.Fatal(err)
	}
	if len(companionSender.Calls()) != 2 {
		t.Fatalf("companion should continue after one segment failure, calls=%#v", companionSender.Calls())
	}
}

func TestDispatchEmitsTurnLifecycleEvents(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	sender := feishu.NewMockSender()
	emitter := &recordingEmitter{}
	mgr := session.NewManager(store, mockengine.New(), sender, session.Options{WorkspaceMode: "work", Emitter: emitter})
	if err := mgr.Dispatch(context.Background(), incoming("m1", "hello")); err != nil {
		t.Fatal(err)
	}
	if len(emitter.events) < 2 {
		t.Fatalf("events = %#v", emitter.events)
	}
	if emitter.events[0].EventType != observability.EventTurnStarted {
		t.Fatalf("first event = %#v", emitter.events[0])
	}
	last := emitter.events[len(emitter.events)-1]
	if last.EventType != observability.EventTurnCompleted || last.AppID != "demo" || last.ChannelKey == "" || last.MessageID != "m1" || last.TurnID == "" {
		t.Fatalf("completed event = %#v", last)
	}
}

func TestManagerCloseCancelsActiveTurn(t *testing.T) {
	store, _ := db.Open(filepath.Join(t.TempDir(), "bot.db"))
	sender := feishu.NewMockSender()
	engine := newBlockingEngine()
	mgr := session.NewManager(store, engine, sender, session.Options{WorkspaceMode: "work"})
	done := make(chan error, 1)
	go func() {
		done <- mgr.Dispatch(context.Background(), incoming("m1", "slow"))
	}()
	engine.waitEntered(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := mgr.Close(ctx); err != nil {
		t.Fatalf("Close() should cancel active turn before timeout: %v", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("dispatch should return cancellation error")
		}
	case <-time.After(time.Second):
		t.Fatal("dispatch did not return after manager close")
	}
}

type recordingEngine struct {
	called bool
	prompt string
}

type recordingEmitter struct {
	events []observability.Event
}

func (e *recordingEmitter) Emit(ctx context.Context, ev observability.Event) {
	e.events = append(e.events, ev)
}

type eventEngine struct {
	events []engine.TurnEvent
}

func (e *eventEngine) SendTurn(ctx context.Context, req engine.TurnRequest) (engine.EventStream, error) {
	return engine.NewSliceStream(e.events), nil
}

type blockingEngine struct {
	mu       sync.Mutex
	enter    chan string
	release  chan struct{}
	recorded []string
}

func newBlockingEngine() *blockingEngine {
	return &blockingEngine{enter: make(chan string, 10), release: make(chan struct{}, 10)}
}

func (e *blockingEngine) SendTurn(ctx context.Context, req engine.TurnRequest) (engine.EventStream, error) {
	e.mu.Lock()
	e.recorded = append(e.recorded, req.Prompt)
	e.mu.Unlock()
	e.enter <- req.Prompt
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.release:
	}
	threadID := req.ThreadID
	if threadID == "" {
		threadID = "thread-blocking"
	}
	return engine.NewSliceStream([]engine.TurnEvent{
		{Type: engine.EventTurnStarted, ThreadID: threadID},
		{Type: engine.EventDelta, ThreadID: threadID, Text: "ok"},
		{Type: engine.EventCompleted, ThreadID: threadID, InputTokens: 1, OutputTokens: 1},
	}), nil
}

func (e *blockingEngine) waitEntered(t *testing.T, want int) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		e.mu.Lock()
		got := len(e.recorded)
		e.mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("engine entered %d times, want %d", got, want)
		case <-e.enter:
		}
	}
}

func (e *blockingEngine) releaseOne() {
	e.release <- struct{}{}
}

func (e *blockingEngine) prompts() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.recorded))
	copy(out, e.recorded)
	return out
}

func (e *recordingEngine) SendTurn(ctx context.Context, req engine.TurnRequest) (engine.EventStream, error) {
	e.called = true
	e.prompt = req.Prompt
	threadID := req.ThreadID
	if threadID == "" {
		threadID = "thread-recorded"
	}
	return engine.NewSliceStream([]engine.TurnEvent{
		{Type: engine.EventTurnStarted, ThreadID: threadID},
		{Type: engine.EventDelta, ThreadID: threadID, Text: "ok"},
		{Type: engine.EventCompleted, ThreadID: threadID, InputTokens: 1, OutputTokens: 1},
	}), nil
}

func incoming(id, prompt string) feishu.IncomingMessage {
	return feishu.IncomingMessage{
		AppID: "demo", ChatType: "p2p", ChatID: "oc_p2p", ChannelKey: "p2p:oc_p2p:demo",
		SenderID: "ou_user", MessageID: id, Prompt: prompt, ReceiveID: "ou_user", ReceiveType: "open_id",
	}
}
