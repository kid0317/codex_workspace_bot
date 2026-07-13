package output_test

import (
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/output"
)

func TestMapperAllowsOnlyCompletedAgentMessagePhasesAndSanitizes(t *testing.T) {
	mapper := output.NewMapper()
	ignored := []output.Item{
		{ID: "reasoning", Type: "reasoning", Phase: "commentary", Text: "hidden"},
		{ID: "empty", Type: "agentMessage", Phase: "commentary", Text: ""},
		{ID: "unknown", Type: "agentMessage", Phase: "analysis", Text: "hidden"},
	}
	for _, item := range ignored {
		if _, ok := mapper.Accept(item); ok {
			t.Fatalf("Accept(%+v) = visible, want ignored", item)
		}
	}
	visible, ok := mapper.Accept(output.Item{ID: "commentary-1", Type: "agentMessage", Phase: "commentary", Text: "<at id=all>& hello"})
	if !ok {
		t.Fatal("commentary item was ignored")
	}
	if visible.Text != "&lt;at id=all&gt;&amp; hello" {
		t.Fatalf("sanitized text = %q", visible.Text)
	}
	if _, ok := mapper.Accept(output.Item{ID: "commentary-1", Type: "agentMessage", Phase: "commentary", Text: "duplicate"}); ok {
		t.Fatal("duplicate item was accepted")
	}
}

func TestProjectionKeepsCommentaryAndLatestFinalSeparate(t *testing.T) {
	projection := output.NewProjection()
	projection.Apply(output.Presentation{ID: "c1", Phase: output.Commentary, Text: "one"})
	projection.Apply(output.Presentation{ID: "c2", Phase: output.Commentary, Text: "two"})
	projection.Apply(output.Presentation{ID: "f1", Phase: output.FinalAnswer, Text: "first final"})
	projection.Apply(output.Presentation{ID: "f2", Phase: output.FinalAnswer, Text: "latest final"})
	if got := projection.Progress(); got != "one\n\ntwo" {
		t.Fatalf("progress = %q", got)
	}
	if got := projection.Final(); got != "latest final" {
		t.Fatalf("final = %q", got)
	}
}
