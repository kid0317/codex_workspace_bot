package observability

import "testing"

func TestUsageDetailsUsesMutuallyExclusiveBuckets(t *testing.T) {
	raw := Usage{InputTokens: 100, OutputTokens: 40, CachedInputTokens: 60, ReasoningOutputTokens: 15, TotalTokens: 140}
	details, ok, reason := raw.UsageDetails()
	if !ok || reason != "" {
		t.Fatalf("UsageDetails() ok=%t reason=%q", ok, reason)
	}
	if details.Input != 40 || details.InputCachedTokens != 60 || details.Output != 25 || details.OutputReasoningTokens != 15 || details.Total != 140 {
		t.Fatalf("UsageDetails() = %#v", details)
	}
}

func TestUsageDetailsRejectsInconsistentOrInclusiveCounters(t *testing.T) {
	for _, raw := range []Usage{
		{InputTokens: 10, OutputTokens: 4, CachedInputTokens: 11, TotalTokens: 14},
		{InputTokens: 10, OutputTokens: 4, ReasoningOutputTokens: 5, TotalTokens: 14},
		{InputTokens: 10, OutputTokens: 4, TotalTokens: 13},
		{InputTokens: -1, OutputTokens: 4, TotalTokens: 3},
	} {
		if _, ok, reason := raw.UsageDetails(); ok || reason == "" {
			t.Fatalf("UsageDetails(%#v) ok=%t reason=%q, want rejected", raw, ok, reason)
		}
	}
}

func TestLoopUsageAccumulatorAddsEveryUniqueMonotonicSnapshotDelta(t *testing.T) {
	var accumulator LoopUsageAccumulator
	for _, input := range []struct {
		generation uint64
		seq        uint64
		usage      Usage
		wantAdded  bool
	}{
		{1, 10, Usage{InputTokens: 100, OutputTokens: 10, CachedInputTokens: 60, ReasoningOutputTokens: 2, TotalTokens: 110}, false},
		{1, 11, Usage{InputTokens: 120, OutputTokens: 15, CachedInputTokens: 70, ReasoningOutputTokens: 3, TotalTokens: 135}, true},
		{1, 12, Usage{InputTokens: 150, OutputTokens: 30, CachedInputTokens: 90, ReasoningOutputTokens: 8, TotalTokens: 180}, true},
		{1, 12, Usage{InputTokens: 150, OutputTokens: 30, CachedInputTokens: 90, ReasoningOutputTokens: 8, TotalTokens: 180}, false},
	} {
		_, added, err := accumulator.Observe(input.generation, input.seq, input.usage)
		if err != nil || added != input.wantAdded {
			t.Fatalf("Observe(%d,%d) added=%t err=%v, want added=%t", input.generation, input.seq, added, err, input.wantAdded)
		}
	}
	got := accumulator.Total()
	want := Usage{InputTokens: 50, OutputTokens: 20, CachedInputTokens: 30, ReasoningOutputTokens: 6, TotalTokens: 70}
	if got != want {
		t.Fatalf("Total() = %#v, want %#v", got, want)
	}
}

func TestLoopUsageAccumulatorRejectsCounterRegressionWithoutAdding(t *testing.T) {
	var accumulator LoopUsageAccumulator
	_, _, _ = accumulator.Observe(1, 1, Usage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120})
	if _, _, err := accumulator.Observe(1, 2, Usage{InputTokens: 99, OutputTokens: 20, TotalTokens: 119}); err == nil {
		t.Fatal("Observe() error = nil, want counter regression")
	}
	if got := accumulator.Total(); got != (Usage{}) {
		t.Fatalf("Total() after rejected snapshot = %#v, want zero", got)
	}
}
