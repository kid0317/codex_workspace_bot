package output_test

import (
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/output"
)

func TestCompanionLexerSegmentsCompatibleMarkersAndPreservesProtectedText(t *testing.T) {
	lexed, err := output.LexCompanion("A［［ s E N D ］］B\n```go\n[[SEND]]\n```\n\\[[SEND]]")
	if err != nil {
		t.Fatal(err)
	}
	segments := output.SplitCompanion(lexed.SegmenterInput, lexed.Delimiter)
	if len(segments) != 2 || segments[0] != "A" || segments[1] != "B\n```go\n[[SEND]]\n```\n[[SEND]]" {
		t.Fatalf("segments = %#v", segments)
	}
	if got := lexed.StorageText; got != "A\nB\n```go\n[[SEND]]\n```\n[[SEND]]" {
		t.Fatalf("storage text = %q", got)
	}
}

func TestCompanionLexerMarkerOnlyHasNoUsableSegments(t *testing.T) {
	lexed, err := output.LexCompanion("[[SEND]][[SEND]]")
	if err != nil {
		t.Fatal(err)
	}
	if segments := output.SplitCompanion(lexed.SegmenterInput, lexed.Delimiter); len(segments) != 0 {
		t.Fatalf("segments = %#v, want none", segments)
	}
}

func TestCompanionLexerAcceptsStandaloneSingleBracketMarker(t *testing.T) {
	lexed, err := output.LexCompanion("first\n[SEND]\nsecond")
	if err != nil {
		t.Fatal(err)
	}
	if got := output.SplitCompanion(lexed.SegmenterInput, lexed.Delimiter); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("segments = %#v", got)
	}
}

func TestSplitCompanionFallsBackToParagraphsAndRuneLimit(t *testing.T) {
	if got := output.SplitCompanion("first paragraph\n\nsecond paragraph", "missing"); len(got) != 2 || got[0] != "first paragraph" || got[1] != "second paragraph" {
		t.Fatalf("paragraph segments = %#v", got)
	}
	bounded := "这是一段会在八十字符边界内被安全切开的文本" + "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz"
	for _, segment := range output.SplitCompanion(bounded, "missing") {
		if len([]rune(segment)) > 80 {
			t.Fatalf("bounded segment is too long: %d", len([]rune(segment)))
		}
	}
	fragmented := "短句。"
	for len([]rune(fragmented)) <= 80 {
		fragmented += "短句。"
	}
	if got := output.SplitCompanion(fragmented, "missing"); len(got) != 1 || got[0] != fragmented {
		t.Fatalf("overly fragmented fallback = %#v", got)
	}
}
