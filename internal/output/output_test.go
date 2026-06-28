package output_test

import (
	"context"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/output"
)

func TestProcessFiltersBeforeSegmentationAndStripsStoredMarkers(t *testing.T) {
	filter := output.FilterFunc(func(ctx context.Context, text string) (string, error) {
		return "keep[[SEND]]second", nil
	})
	got, err := output.Process(context.Background(), "ignored", filter)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if len(got.Segments) != 2 || got.Segments[0] != "keep" || got.Segments[1] != "second" {
		t.Fatalf("segments = %#v", got.Segments)
	}
	if got.StoredText != "keep\nsecond" {
		t.Fatalf("StoredText = %q", got.StoredText)
	}
}

func TestProcessEmptyOutputIsHandledFailure(t *testing.T) {
	_, err := output.Process(context.Background(), "  [[SEND]]  ", nil)
	if err == nil {
		t.Fatal("Process() should reject empty final output")
	}
}
