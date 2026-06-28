package output_test

import (
	"context"
	"os"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/output"
	"gopkg.in/yaml.v3"
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

func TestOutputFilterFixturesDriveBehavior(t *testing.T) {
	var sendCases []struct {
		Input    string   `yaml:"input"`
		Segments []string `yaml:"segments"`
	}
	readYAML(t, "../../testdata/output_filter/send_marker_cases.yaml", &sendCases)
	for _, tc := range sendCases {
		got := output.SplitSegments(tc.Input)
		if len(got) != len(tc.Segments) {
			t.Fatalf("SplitSegments(%q) = %#v, want %#v", tc.Input, got, tc.Segments)
		}
		for i := range got {
			if got[i] != tc.Segments[i] {
				t.Fatalf("SplitSegments(%q) = %#v, want %#v", tc.Input, got, tc.Segments)
			}
		}
	}

	var gateCases []struct {
		Name  string `yaml:"name"`
		Input string `yaml:"input"`
		Error string `yaml:"error"`
	}
	readYAML(t, "../../testdata/output_filter/gate_cases.yaml", &gateCases)
	for _, tc := range gateCases {
		_, err := output.Process(context.Background(), tc.Input, nil)
		if (tc.Error != "") != (err != nil) {
			t.Fatalf("Process(%q) error=%v, want marker %q", tc.Input, err, tc.Error)
		}
	}
}

func readYAML(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}
