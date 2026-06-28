package observability_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/observability"
)

func TestLifecycleEventHasStableFieldsAndPreservesZeroUsage(t *testing.T) {
	ev := observability.Event{
		AppID: "demo", ChannelKey: "p2p:oc:demo", SessionID: "s1", EngineThreadID: "thread",
		MessageID: "m1", TaskID: "demo/task", TurnID: "turn", EventType: observability.EventTurnCompleted,
		DurationMS: 12, InputTokens: 0, OutputTokens: 0, At: time.Unix(1, 0),
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"app_id", "channel_key", "session_id", "engine_thread_id", "message_id", "task_id", "turn_id", "event_type", "duration_ms", "input_tokens", "output_tokens"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing field %s in %s", key, data)
		}
	}
	if fields["input_tokens"].(float64) != 0 || fields["output_tokens"].(float64) != 0 {
		t.Fatalf("zero usage not preserved: %s", data)
	}
}
