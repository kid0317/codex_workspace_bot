package router_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/router"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

func TestClassifyCommandAcceptsOnlyS06Grammar(t *testing.T) {
	cases := []struct {
		name, text, kind, argument string
	}{
		{"normal", "hello", "normal", ""},
		{"full width slash stays normal", "／status", "normal", ""},
		{"new trims unicode", "\u2003/NeW\u00a0", "new", ""},
		{"cancel", "/cancel", "cancel", ""},
		{"stop alias", "/STOP", "cancel", ""},
		{"status", "/status", "status", ""},
		{"help", "/help", "help", ""},
		{"goal preserves remainder", "/goal  建立稳定交付  ", "goal", "建立稳定交付"},
		{"empty goal invalid", "/goal\t", "invalid", ""},
		{"new arguments invalid", "/new now", "invalid", ""},
		{"unknown invalid", "/x test", "invalid", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := router.ClassifyCommand(tc.text)
			if string(got.Kind) != tc.kind || got.Argument != tc.argument {
				t.Fatalf("ClassifyCommand(%q) = %#v, want kind=%q argument=%q", tc.text, got, tc.kind, tc.argument)
			}
		})
	}
}

func TestHandleGoalPersistsOnlyRedactedReceiptMetadata(t *testing.T) {
	store, dispatcher := &fakeStore{}, &fakeDispatcher{}
	receivedAt := time.Date(2026, 7, 13, 8, 30, 0, 123000000, time.UTC)
	err := router.New(store, dispatcher).Handle(context.Background(), router.Incoming{
		App:     router.App{ID: "app-1", Name: "health", WorkspaceDir: "/work", Model: "model", Effort: "medium"},
		EventID: "event-goal", MessageID: "om-goal", ChatType: "group", ChatID: "oc-group", SenderOpenID: "ou-user",
		Text: "/goal sentinel-private-objective", ReceivedAt: receivedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.input.UserContent != "/goal [redacted]" || store.input.CommandKind != "goal" || store.input.CommandPayloadSHA256 == "" || store.input.CommandPayloadBytes != len("sentinel-private-objective") || !store.input.ReceivedAt.Equal(receivedAt) {
		t.Fatalf("goal receipt = %#v", store.input)
	}
}

type fakeStore struct {
	duplicate, failed bool
	input             storage.MessageInput
}

func (s *fakeStore) PersistIncoming(_ context.Context, appID, chatType, chatID string, input storage.MessageInput) (storage.MessageRecord, bool, error) {
	if appID == "" || chatType == "" || chatID == "" {
		return storage.MessageRecord{}, false, errors.New("missing chat group key")
	}
	s.input = input
	return storage.MessageRecord{ID: "message-1", TraceID: input.TraceID, ChatGroupID: "group-1"}, s.duplicate, nil
}

type fakeProtector struct{}

func (fakeProtector) Seal(_, attachmentID, _, resourceKey string) ([]byte, int, error) {
	return []byte("sealed:" + attachmentID + ":" + resourceKey), 1, nil
}
func (s *fakeStore) FailMessage(_ context.Context, _ string, _ string, _ string, _ int64) error {
	s.failed = true
	return nil
}
func (s *fakeStore) CompleteMessage(context.Context, string, string, string, int64) error { return nil }

type fakeDispatcher struct {
	job worker.Message
	err error
}

func (d *fakeDispatcher) Cancel(context.Context, worker.Key) error { return nil }

type unavailableRuntime struct{}

func (unavailableRuntime) IsReady() bool { return false }

type fakeResetter struct{ groupID string }

func (r *fakeResetter) NewSession(_ context.Context, groupID string) error {
	r.groupID = groupID
	return nil
}

func (d *fakeDispatcher) Accept(_ context.Context, job worker.Message) error {
	d.job = job
	return d.err
}

func TestHandleP2PTextQueuesByOpenID(t *testing.T) {
	store, dispatcher := &fakeStore{}, &fakeDispatcher{}
	handler := router.New(store, dispatcher)
	err := handler.Handle(context.Background(), router.Incoming{App: router.App{ID: "app-1", Name: "health", WorkspaceDir: "/work", Model: "model", Effort: "medium"}, EventID: "event-1", MessageID: "om_user_1", ChatType: "p2p", ChatID: "oc_p2p", SenderOpenID: "ou_user", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got := dispatcher.job.Key; got != worker.P2PKey("ou_user", "app-1") {
		t.Fatalf("key = %#v", got)
	}
	if dispatcher.job.Reply != (worker.ReplyTarget{ID: "ou_user", Type: "open_id"}) {
		t.Fatalf("reply = %#v", dispatcher.job.Reply)
	}
}

func TestHandleGroupTextQueuesByChatID(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	err := router.New(&fakeStore{}, dispatcher).Handle(context.Background(), router.Incoming{App: router.App{ID: "app-1", Name: "health", WorkspaceDir: "/work", Model: "model", Effort: "medium"}, EventID: "event-1", MessageID: "om_user_1", ChatType: "group", ChatID: "oc_group", SenderOpenID: "ou_user", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got := dispatcher.job.Key; got != worker.GroupKey("oc_group", "app-1") {
		t.Fatalf("key = %#v", got)
	}
	if dispatcher.job.Reply != (worker.ReplyTarget{ID: "oc_group", Type: "chat_id"}) {
		t.Fatalf("reply = %#v", dispatcher.job.Reply)
	}
}

func TestHandleAttachmentStagesEncryptedReferenceAndQueuesExclusiveBatch(t *testing.T) {
	store, dispatcher := &fakeStore{}, &fakeDispatcher{}
	handler := router.New(store, dispatcher)
	handler.SetAttachmentProtector(fakeProtector{})
	err := handler.Handle(context.Background(), router.Incoming{
		App:     router.App{ID: "app-1", Name: "health", WorkspaceDir: "/work", Model: "model", Effort: "medium"},
		EventID: "event-file", MessageID: "om-file", ChatType: "group", ChatID: "oc_group",
		Attachments: []router.AttachmentReference{{Kind: storage.AttachmentFile, ResourceKey: "file-key", OriginalName: "report.pdf"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.input.Attachments) != 1 || store.input.Attachments[0].SourceRefKeyVersion != 1 || string(store.input.Attachments[0].SourceResourceRefEnc) == "file-key" {
		t.Fatalf("staged attachment = %#v", store.input.Attachments)
	}
	if !dispatcher.job.HasRequiredAttachment || len(dispatcher.job.AttachmentIDs) != 1 || dispatcher.job.Query != "" {
		t.Fatalf("worker job = %#v", dispatcher.job)
	}
}

func TestHandleDuplicateDoesNotQueue(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	err := router.New(&fakeStore{duplicate: true}, dispatcher).Handle(context.Background(), router.Incoming{App: router.App{ID: "app-1", Name: "health", WorkspaceDir: "/work", Model: "model", Effort: "medium"}, EventID: "event-1", MessageID: "om_user_1", ChatType: "group", ChatID: "oc_group", SenderOpenID: "ou_user", Text: "hello"})
	if !errors.Is(err, router.ErrDuplicate) {
		t.Fatalf("Handle() = %v", err)
	}
	if dispatcher.job.ID != "" {
		t.Fatal("duplicate must not queue")
	}
}

func TestUnavailableAppServerPersistsFailureWithoutQueueing(t *testing.T) {
	store, dispatcher := &fakeStore{}, &fakeDispatcher{}
	handler := router.New(store, dispatcher)
	handler.SetAvailability(unavailableRuntime{})
	err := handler.Handle(context.Background(), router.Incoming{App: router.App{ID: "app-1", Name: "health", WorkspaceDir: "/work", Model: "model", Effort: "medium"}, EventID: "event-1", MessageID: "om_user_1", ChatType: "group", ChatID: "oc_group", SenderOpenID: "ou_user", Text: "hello"})
	if err == nil || !store.failed {
		t.Fatalf("Handle()=%v, failed=%v", err, store.failed)
	}
	if dispatcher.job.ID != "" {
		t.Fatal("unavailable app server must not enqueue")
	}
}

func TestCancelCommandPersistsThenCancelsWithoutQueueing(t *testing.T) {
	store, dispatcher := &fakeStore{}, &fakeDispatcher{}
	handler := router.New(store, dispatcher)
	err := handler.Handle(context.Background(), router.Incoming{App: router.App{ID: "app-1", Name: "health", WorkspaceDir: "/work", Model: "model", Effort: "medium"}, EventID: "event-cancel", MessageID: "om_user_cancel", ChatType: "group", ChatID: "oc_group", SenderOpenID: "ou_user", Text: "/cancel"})
	if err != nil {
		t.Fatal(err)
	}
	if dispatcher.job.ID != "" {
		t.Fatal("cancel command must not enqueue a normal turn")
	}
}

func TestNewCommandStopsThenResetsWithoutQueueing(t *testing.T) {
	store, dispatcher, resetter := &fakeStore{}, &fakeDispatcher{}, &fakeResetter{}
	handler := router.New(store, dispatcher)
	handler.SetSessionResetter(resetter)
	err := handler.Handle(context.Background(), router.Incoming{App: router.App{ID: "app-1", Name: "health", WorkspaceDir: "/work", Model: "model", Effort: "medium"}, EventID: "event-new", MessageID: "om_user_new", ChatType: "group", ChatID: "oc_group", SenderOpenID: "ou_user", Text: "/new"})
	if err != nil {
		t.Fatal(err)
	}
	if dispatcher.job.ID != "" || resetter.groupID != "group-1" {
		t.Fatalf("new queued=%q reset group=%q", dispatcher.job.ID, resetter.groupID)
	}
}
