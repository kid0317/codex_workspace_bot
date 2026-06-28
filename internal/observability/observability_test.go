package observability_test

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
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

func TestTelemetryFixturesPreserveZeroUsageAndFailOpenMalformedRows(t *testing.T) {
	rows := readJSONLines(t, "../../testdata/telemetry/langfuse_dryrun_rows.jsonl")
	if len(rows) != 2 {
		t.Fatalf("telemetry rows = %d, want 2", len(rows))
	}
	if rows[0]["input_tokens"].(float64) != 0 || rows[0]["output_tokens"].(float64) != 0 {
		t.Fatalf("zero usage row not preserved: %#v", rows[0])
	}

	data, err := os.ReadFile("../../testdata/telemetry/malformed_rows.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var valid int
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err == nil {
			valid++
		}
	}
	if valid != 0 {
		t.Fatalf("malformed fixture unexpectedly had %d valid rows", valid)
	}
}

func readJSONLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var rows []map[string]any
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}
