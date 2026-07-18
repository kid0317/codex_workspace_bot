package observability

import (
	"fmt"
)

// Usage preserves the counters exactly as the App Server sent them. Cache and
// reasoning counters are included in input and output respectively, so they
// must not be passed alongside their containing counters as Langfuse usage
// buckets.
type Usage struct {
	InputTokens           int64
	OutputTokens          int64
	CachedInputTokens     int64
	ReasoningOutputTokens int64
	TotalTokens           int64
}

// UsageDetails contains the mutually-exclusive Langfuse usage buckets.
// Raw Usage remains the source of truth and is exported separately.
type UsageDetails struct {
	Input                 int64 `json:"input"`
	InputCachedTokens     int64 `json:"input_cached_tokens"`
	Output                int64 `json:"output"`
	OutputReasoningTokens int64 `json:"output_reasoning_tokens"`
	Total                 int64 `json:"total"`
}

// SessionUsageKey is the real Feishu conversation identity. It intentionally
// remains plaintext because S08's self-hosted observability surfaces are for
// the operator's personal local debugging.
type SessionUsageKey struct {
	AppID, ChatType, ChatID string
}

func (k SessionUsageKey) Valid() bool {
	return k.AppID != "" && k.ChatType != "" && k.ChatID != ""
}

// TurnUsageRecord is one authoritative completed-Turn delta. Storage merges it
// into that Thread's effective cumulative total before deriving the session.
type TurnUsageRecord struct {
	TraceID, ThreadID, TurnID string
	Session                   SessionUsageKey
	Usage                     Usage
}

// ThreadUsageSnapshotRecord is the exact cumulative counter emitted by
// thread/tokenUsage/updated. Some App Server versions omit usage from
// turn/completed; storage accepts it only as a monotonic high-water mark for
// that Thread before deriving the session total.
type ThreadUsageSnapshotRecord struct {
	TraceID, ThreadID, TurnID string
	Session                   SessionUsageKey
	Usage                     Usage
}

type SessionUsageTotal struct {
	Usage
	CompletedTurnCount int64
}

// UsageDetails maps an App Server usage payload only when its inclusion
// relations can be proven. Future wire-schema changes are retained as raw
// counters rather than guessed into a potentially double-counted bucket.
func (u Usage) UsageDetails() (UsageDetails, bool, string) {
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.CachedInputTokens < 0 || u.ReasoningOutputTokens < 0 || u.TotalTokens < 0 {
		return UsageDetails{}, false, "negative_counter"
	}
	if u.CachedInputTokens > u.InputTokens {
		return UsageDetails{}, false, "cached_input_exceeds_input"
	}
	if u.ReasoningOutputTokens > u.OutputTokens {
		return UsageDetails{}, false, "reasoning_output_exceeds_output"
	}
	if u.TotalTokens != u.InputTokens+u.OutputTokens {
		return UsageDetails{}, false, "total_tokens_mismatch"
	}
	return UsageDetails{
		Input:                 u.InputTokens - u.CachedInputTokens,
		InputCachedTokens:     u.CachedInputTokens,
		Output:                u.OutputTokens - u.ReasoningOutputTokens,
		OutputReasoningTokens: u.ReasoningOutputTokens,
		Total:                 u.TotalTokens,
	}, true, ""
}

func (u Usage) subtract(previous Usage) (Usage, error) {
	delta := Usage{
		InputTokens:           u.InputTokens - previous.InputTokens,
		OutputTokens:          u.OutputTokens - previous.OutputTokens,
		CachedInputTokens:     u.CachedInputTokens - previous.CachedInputTokens,
		ReasoningOutputTokens: u.ReasoningOutputTokens - previous.ReasoningOutputTokens,
		TotalTokens:           u.TotalTokens - previous.TotalTokens,
	}
	if delta.InputTokens < 0 || delta.OutputTokens < 0 || delta.CachedInputTokens < 0 || delta.ReasoningOutputTokens < 0 || delta.TotalTokens < 0 {
		return Usage{}, fmt.Errorf("token usage counter regressed")
	}
	return delta, nil
}

func (u *Usage) add(other Usage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CachedInputTokens += other.CachedInputTokens
	u.ReasoningOutputTokens += other.ReasoningOutputTokens
	u.TotalTokens += other.TotalTokens
}

// LoopUsageAccumulator assigns the observed deltas of an App Server's
// thread-level snapshots to one protocol-visible Agent loop. The first
// snapshot establishes the baseline and contributes no usage. Duplicate
// `(generation, seq)` snapshots are ignored exactly once.
type LoopUsageAccumulator struct {
	seen     map[usageSnapshotKey]struct{}
	baseline *Usage
	total    Usage
}

type usageSnapshotKey struct {
	generation uint64
	seq        uint64
}

// Observe returns the raw delta and whether it was included in the loop.
func (a *LoopUsageAccumulator) Observe(generation, seq uint64, current Usage) (Usage, bool, error) {
	if a.seen == nil {
		a.seen = make(map[usageSnapshotKey]struct{})
	}
	key := usageSnapshotKey{generation: generation, seq: seq}
	if _, exists := a.seen[key]; exists {
		return Usage{}, false, nil
	}
	a.seen[key] = struct{}{}
	if a.baseline == nil {
		baseline := current
		a.baseline = &baseline
		return Usage{}, false, nil
	}
	delta, err := current.subtract(*a.baseline)
	if err != nil {
		return Usage{}, false, err
	}
	baseline := current
	a.baseline = &baseline
	a.total.add(delta)
	return delta, true, nil
}

func (a *LoopUsageAccumulator) Total() Usage { return a.total }
