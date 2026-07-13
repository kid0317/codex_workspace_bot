package codexapp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Timeline keeps the S03 local-only debug evidence. Its writes are synchronous:
// a later dispatch never runs if raw or event evidence could not be persisted.
type Timeline struct {
	mu                  sync.Mutex
	seq                 uint64
	started             time.Time
	raw, event, outcome *os.File
	snapshot            func(Event) map[string]any
}

func NewTimeline(dir, processStart string, snapshot func(Event) map[string]any) (*Timeline, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create debug log directory: %w", err)
	}
	open := func(prefix, suffix string) (*os.File, error) {
		return os.OpenFile(filepath.Join(dir, prefix+processStart+suffix), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	}
	raw, err := open("appserver-raw-", ".ndjson")
	if err != nil {
		return nil, err
	}
	event, err := open("appserver-event-", ".jsonl")
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	outcome, err := open("appserver-outcome-", ".jsonl")
	if err != nil {
		_ = raw.Close()
		_ = event.Close()
		return nil, err
	}
	return &Timeline{raw: raw, event: event, outcome: outcome, snapshot: snapshot, started: time.Now()}, nil
}

func (t *Timeline) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	var first error
	for _, file := range []*os.File{t.raw, t.event, t.outcome} {
		if file != nil {
			if err := file.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	t.raw, t.event, t.outcome = nil, nil, nil
	return first
}

func (t *Timeline) BeforeDispatch(event *Event) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	route := t.snapshot(*event)
	entry := map[string]any{"seq": event.Seq, "observed_at": now.UTC().Format(time.RFC3339Nano), "elapsed_ns": now.Sub(t.started).Nanoseconds(), "direction": "server_to_client", "message_class": event.Class, "class": event.Class, "method": nullableID(event.Method), "rpc_id": json.RawMessage(event.ID), "generation": nil, "app_id": nil, "channel_key": nil, "chat_group_id": nil, "attempt_id": nil, "thread_id": nil, "turn_id": nil, "item_id": nil, "route_snapshot": route}
	for _, field := range []string{"generation", "app_id", "channel_key", "chat_group_id", "attempt_id", "thread_id", "turn_id", "item_id"} {
		if value, ok := route[field]; ok {
			entry[field] = value
		}
	}
	return writeJSONSync(t.event, entry)
}

func (t *Timeline) RecordRaw(raw []byte) (uint64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	if err := writeSync(t.raw, append(raw, '\n')); err != nil {
		return 0, err
	}
	return t.seq, nil
}

func (t *Timeline) ProtocolError(seq uint64, err error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if writeErr := writeJSONSync(t.event, map[string]any{"seq": seq, "observed_at": now.UTC().Format(time.RFC3339Nano), "elapsed_ns": now.Sub(t.started).Nanoseconds(), "direction": "server_to_client", "message_class": "protocol_error", "class": "protocol_error", "method": nil, "rpc_id": nil, "generation": nil, "app_id": nil, "channel_key": nil, "chat_group_id": nil, "attempt_id": nil, "thread_id": nil, "turn_id": nil, "item_id": nil, "route_snapshot": nil, "error": err.Error()}); writeErr != nil {
		return writeErr
	}
	return writeJSONSync(t.outcome, map[string]any{"seq": seq, "outcome_at": now.UTC().Format(time.RFC3339Nano), "elapsed_ns": now.Sub(t.started).Nanoseconds(), "route_state": "protocol_error", "dispatch_result": "protocol_error", "error": err.Error()})
}

func (t *Timeline) AfterDispatch(event *Event, result string, dispatchErr error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	entry := map[string]any{"seq": event.Seq, "outcome_at": now.UTC().Format(time.RFC3339Nano), "elapsed_ns": now.Sub(t.started).Nanoseconds(), "route_state": result, "dispatch_result": result, "error": nil}
	if dispatchErr != nil {
		entry["error"] = dispatchErr.Error()
	}
	return writeJSONSync(t.outcome, entry)
}

func writeJSONSync(file *os.File, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeSync(file, append(encoded, '\n'))
}
func writeSync(file *os.File, value []byte) error {
	if file == nil {
		return fmt.Errorf("debug evidence writer is closed")
	}
	if _, err := file.Write(value); err != nil {
		return err
	}
	return file.Sync()
}
