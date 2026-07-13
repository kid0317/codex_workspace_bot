package feishu_test

import (
	"encoding/json"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/feishu"
)

func TestRenderWorkCardUsesSeparateFinalAndProgressElements(t *testing.T) {
	encoded, err := feishu.RenderWorkCardJSON("answer", "progress", "已完成", true)
	if err != nil {
		t.Fatal(err)
	}
	var card map[string]any
	if err := json.Unmarshal(encoded, &card); err != nil {
		t.Fatal(err)
	}
	config := card["config"].(map[string]any)
	if config["streaming_mode"] != false || config["summary"] != nil {
		t.Fatalf("config = %#v", config)
	}
	if string(encoded) == "" || !containsAll(string(encoded), "final_text", "progress_text", "answer", "progress") {
		t.Fatalf("card = %s", encoded)
	}
}

func TestRenderWorkCardKeepsVisiblePlaceholderAndCompletionState(t *testing.T) {
	processing, err := feishu.RenderWorkCardJSON("", "first progress", "生成中…", false)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(processing), "思考中…", "first progress") || contains(string(processing), "Codex ·") || contains(string(processing), "\"summary\"") {
		t.Fatalf("processing card does not preserve visible state: %s", processing)
	}
	completed, err := feishu.RenderWorkCardJSON("final answer", "first progress", "已完成", true)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(completed), "final answer", "first progress") || contains(string(completed), "Codex ·") {
		t.Fatalf("completed card does not expose completion state: %s", completed)
	}
}

func TestRenderWorkCardPutsProgressBeforeFinalAnswer(t *testing.T) {
	encoded, err := feishu.RenderWorkCardJSON("final answer", "progress update", "已完成", true)
	if err != nil {
		t.Fatal(err)
	}
	var card struct {
		Body struct {
			Elements []struct {
				ElementID string `json:"element_id"`
				Tag       string `json:"tag"`
			} `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal(encoded, &card); err != nil {
		t.Fatal(err)
	}
	if len(card.Body.Elements) < 3 || card.Body.Elements[0].ElementID != "progress_text" || card.Body.Elements[1].Tag != "hr" || card.Body.Elements[2].ElementID != "final_text" {
		t.Fatalf("element order = %#v, want progress_text, divider, then final_text", card.Body.Elements)
	}
}

func TestRenderWorkCardStylesProgressAsGreyItalicMarkdown(t *testing.T) {
	encoded, err := feishu.RenderWorkCardJSON("answer", "progress update", "生成中…", false)
	if err != nil {
		t.Fatal(err)
	}
	var card struct {
		Header any `json:"header"`
		Body   struct {
			Elements []struct {
				Content   string `json:"content"`
				FontColor string `json:"font_color"`
			} `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal(encoded, &card); err != nil {
		t.Fatal(err)
	}
	if card.Header != nil || len(card.Body.Elements) == 0 || card.Body.Elements[0].Content != "*progress update*" || card.Body.Elements[0].FontColor != "grey" || contains(string(encoded), "progress_panel") || contains(string(encoded), "<font") {
		t.Fatalf("progress styling missing from %s", encoded)
	}
}

func containsAll(value string, values ...string) bool {
	for _, want := range values {
		if !contains(value, want) {
			return false
		}
	}
	return true
}

func contains(value, want string) bool {
	for i := 0; i+len(want) <= len(value); i++ {
		if value[i:i+len(want)] == want {
			return true
		}
	}
	return false
}
