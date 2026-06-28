package feishu_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/feishu"
)

func TestNormalizeEventBuildsChannelKeysReplyTargetsAndRichText(t *testing.T) {
	tests := []struct {
		name            string
		event           feishu.EventFixture
		wantChannelKey  string
		wantReceiveType string
		wantPrompt      string
	}{
		{"p2p", feishu.EventFixture{AppID: "demo", ChatType: "p2p", ChatID: "oc_p2p", SenderID: "ou_user", MessageID: "m1", MessageType: "text", Content: `{"text":"hi"}`}, "p2p:oc_p2p:demo", "open_id", "hi"},
		{"group", feishu.EventFixture{AppID: "demo", ChatType: "group", ChatID: "oc_group", SenderID: "ou_user", MessageID: "m2", MessageType: "text", Content: `{"text":"hello"}`}, "group:oc_group:demo", "chat_id", "hello"},
		{"thread", feishu.EventFixture{AppID: "demo", ChatType: "group", ChatID: "oc_group", ThreadID: "omt_1", SenderID: "ou_user", MessageID: "m3", MessageType: "post", Content: `{"title":"T","content":[[{"tag":"text","text":"A"},{"tag":"img"}],[{"tag":"text","text":"B"}]]}`}, "thread:oc_group:omt_1:demo", "chat_id", "T\nA\nB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := feishu.Normalize(tt.event)
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if got.ChannelKey != tt.wantChannelKey || got.ReceiveType != tt.wantReceiveType || got.Prompt != tt.wantPrompt {
				t.Fatalf("Normalize() = %#v", got)
			}
		})
	}
}

func TestSanitizeFilenameBlocksTraversal(t *testing.T) {
	for _, input := range []string{"../secret.txt", `..\secret.txt`, "", "a/b/c.md"} {
		got := feishu.SanitizeFilename(input)
		if got == "" || got == input || got == "." || got == ".." {
			t.Fatalf("SanitizeFilename(%q) = %q", input, got)
		}
	}
}

func TestLegacyEventFixturesNormalize(t *testing.T) {
	paths, err := filepath.Glob("../../testdata/legacy/events/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("missing legacy event fixtures")
	}
	var normalized int
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var fixture feishu.EventFixture
		if err := json.Unmarshal(data, &fixture); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if fixture.MessageType == "" {
			continue
		}
		msg, err := feishu.Normalize(fixture)
		if err != nil {
			t.Fatalf("%s Normalize error: %v", path, err)
		}
		if msg.ChannelKey == "" || msg.ReceiveID == "" || msg.ReceiveType == "" || strings.TrimSpace(msg.Prompt) == "" {
			t.Fatalf("%s normalized to incomplete message: %#v", path, msg)
		}
		normalized++
	}
	if normalized < 5 {
		t.Fatalf("normalized %d message fixtures, want at least 5", normalized)
	}
}
