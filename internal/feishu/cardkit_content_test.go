package feishu

import (
	"encoding/json"
	"testing"

	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"
)

func TestCardKitTextContentIsRawMarkdown(t *testing.T) {
	body := larkcardkit.NewContentCardElementReqBodyBuilder().Content("进展 <safe>").Build()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded["content"] != "进展 <safe>" {
		t.Fatalf("body = %s, decoded=%#v, err=%v", encoded, decoded, err)
	}
	if decoded["content"] == `{"content":"进展 <safe>"}` {
		t.Fatal("CardKit content must stay raw Markdown, not a serialized JSON object")
	}
}
