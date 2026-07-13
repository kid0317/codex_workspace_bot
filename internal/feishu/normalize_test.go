package feishu_test

import (
	"errors"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/feishu"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
)

func TestNormalizeTextP2P(t *testing.T) {
	incoming, err := feishu.Normalize("cli_expected", feishu.RawEvent{
		HeaderAppID: "cli_expected", EventID: "event-1", MessageID: "om-1", MessageType: "text",
		ChatType: "p2p", ChatID: "oc-1", SenderOpenID: "ou-1", Content: `{"text":"hello"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if incoming.Text != "hello" || incoming.ChatType != "p2p" || incoming.SenderOpenID != "ou-1" {
		t.Fatalf("Normalize() = %#v", incoming)
	}
}

func TestNormalizeRejectsTopicAndHeaderMismatch(t *testing.T) {
	_, err := feishu.Normalize("cli_expected", feishu.RawEvent{HeaderAppID: "cli_other", EventID: "event-1", MessageType: "text", ChatType: "group", ChatID: "oc-1", Content: `{"text":"hello"}`})
	if !errors.Is(err, feishu.ErrIgnored) {
		t.Fatalf("header mismatch error = %v", err)
	}
	_, err = feishu.Normalize("cli_expected", feishu.RawEvent{HeaderAppID: "cli_expected", EventID: "event-1", MessageType: "text", ChatType: "topic_group", ChatID: "oc-1", Content: `{"text":"hello"}`})
	if !errors.Is(err, feishu.ErrIgnored) {
		t.Fatalf("topic error = %v", err)
	}
}

func TestNormalizeParsesImageAndFileReferencesWithoutEncodingThemAsText(t *testing.T) {
	base := feishu.RawEvent{HeaderAppID: "app-1", EventID: "event-1", MessageID: "om-1", ChatType: "group", ChatID: "oc-1"}
	tests := []struct {
		name, messageType, content, kind, key, filename string
	}{
		{name: "image", messageType: "image", content: `{"image_key":"img_v2_abc"}`, kind: "image", key: "img_v2_abc"},
		{name: "file", messageType: "file", content: `{"file_key":"file_v2_xyz","file_name":"report.pdf"}`, kind: "file", key: "file_v2_xyz", filename: "report.pdf"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := base
			raw.MessageType, raw.Content = tc.messageType, tc.content
			incoming, err := feishu.Normalize("app-1", raw)
			if err != nil {
				t.Fatal(err)
			}
			if incoming.Text != "" || len(incoming.Attachments) != 1 {
				t.Fatalf("incoming text=%q attachments=%#v", incoming.Text, incoming.Attachments)
			}
			got := incoming.Attachments[0]
			if string(got.Kind) != tc.kind || got.ResourceKey != tc.key || got.OriginalName != tc.filename {
				t.Fatalf("reference = %#v", got)
			}
		})
	}
}

func TestAttachmentResourceTypeMapsOnlySupportedKinds(t *testing.T) {
	if got, ok := feishu.AttachmentResourceType(storage.AttachmentImage); !ok || got != "image" {
		t.Fatalf("image resource type = %q, %v", got, ok)
	}
	if got, ok := feishu.AttachmentResourceType(storage.AttachmentFile); !ok || got != "file" {
		t.Fatalf("file resource type = %q, %v", got, ok)
	}
	if _, ok := feishu.AttachmentResourceType("audio"); ok {
		t.Fatal("audio must not be downloaded by S05")
	}
}
