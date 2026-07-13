package codexapp_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/codexapp"
)

func TestTimelineKeepsRawEventAndMatchingOutcomeSequence(t *testing.T) {
	dir := t.TempDir()
	timeline, err := codexapp.NewTimeline(dir, "test", func(codexapp.Event) map[string]any {
		return map[string]any{"app_id": "app-1", "attempt_id": "attempt-1"}
	})
	if err != nil {
		t.Fatal(err)
	}
	clientSide, serverSide := net.Pipe()
	client := codexapp.NewClient(clientSide, timeline, nil)
	go func() {
		req, err := codexapp.ReadRequest(serverSide)
		if err == nil {
			_ = codexapp.WriteResponse(serverSide, req.ID, map[string]string{"ok": "yes"})
		}
		_ = serverSide.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.Call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "test"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	client.Close()
	_ = timeline.Close()
	event, err := os.ReadFile(filepath.Join(dir, "appserver-event-test.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := os.ReadFile(filepath.Join(dir, "appserver-outcome-test.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "appserver-raw-test.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\"result\"") || !strings.Contains(string(event), "\"seq\":1") || !strings.Contains(string(outcome), "\"seq\":1") || !strings.Contains(string(event), "attempt-1") {
		t.Fatalf("timeline evidence incomplete: raw=%s event=%s outcome=%s", raw, event, outcome)
	}
}

func TestTimelineRecordsMalformedRawLineBeforeClosingClient(t *testing.T) {
	dir := t.TempDir()
	timeline, err := codexapp.NewTimeline(dir, "malformed", func(codexapp.Event) map[string]any { return nil })
	if err != nil {
		t.Fatal(err)
	}
	clientSide, serverSide := net.Pipe()
	client := codexapp.NewClient(clientSide, timeline, nil)
	_, _ = serverSide.Write([]byte("not-json\n"))
	_ = serverSide.Close()
	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("client remained open after malformed input")
	}
	_ = timeline.Close()
	raw, err := os.ReadFile(filepath.Join(dir, "appserver-raw-malformed.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	event, err := os.ReadFile(filepath.Join(dir, "appserver-event-malformed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := os.ReadFile(filepath.Join(dir, "appserver-outcome-malformed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "not-json") || !strings.Contains(string(event), "\"seq\":1") || !strings.Contains(string(event), "protocol_error") || !strings.Contains(string(outcome), "\"seq\":1") || !strings.Contains(string(outcome), "protocol_error") {
		t.Fatalf("raw=%q event=%q outcome=%q", raw, event, outcome)
	}
}

func TestTimelineWritesFixedSchemaAndExplicitNullError(t *testing.T) {
	dir := t.TempDir()
	timeline, err := codexapp.NewTimeline(dir, "schema", func(codexapp.Event) map[string]any {
		return map[string]any{"generation": uint64(1), "thread_id": "thread-1", "turn_id": "turn-1", "item_id": "item-1"}
	})
	if err != nil {
		t.Fatal(err)
	}
	event := &codexapp.Event{Seq: 1, Class: "notification", Method: "item/agentMessage/delta", Params: []byte(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1"}`)}
	if err := timeline.BeforeDispatch(event); err != nil {
		t.Fatal(err)
	}
	if err := timeline.AfterDispatch(event, "attempt_routed", nil); err != nil {
		t.Fatal(err)
	}
	_ = timeline.Close()
	index, err := os.ReadFile(filepath.Join(dir, "appserver-event-schema.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := os.ReadFile(filepath.Join(dir, "appserver-outcome-schema.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"app_id", "channel_key", "chat_group_id", "attempt_id", "thread_id", "turn_id", "item_id"} {
		if !strings.Contains(string(index), `"`+field+`"`) {
			t.Fatalf("event index missing fixed field %q: %s", field, index)
		}
	}
	if !strings.Contains(string(outcome), `"error":null`) {
		t.Fatalf("outcome must explicitly contain null error: %s", outcome)
	}
}
