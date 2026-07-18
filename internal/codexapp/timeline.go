package codexapp

import (
	"crypto/sha256"
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
	goalThreads         map[string]struct{}
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
	return &Timeline{raw: raw, event: event, outcome: outcome, snapshot: snapshot, started: time.Now(), goalThreads: make(map[string]struct{})}, nil
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
	if err := writeSync(t.raw, append(t.redactGoalRaw(raw), '\n')); err != nil {
		return 0, err
	}
	return t.seq, nil
}

// RegisterGoalThread must run before goal/set so raw responses and the first
// echoed userMessage can be sanitized before they reach debug evidence.
func (t *Timeline) RegisterGoalThread(threadID string) {
	if threadID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.goalThreads[threadID] = struct{}{}
}

func (t *Timeline) UnregisterGoalThread(threadID string) {
	if threadID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.goalThreads, threadID)
}

func (t *Timeline) redactGoalRaw(raw []byte) []byte {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return raw
	}
	redactObjectiveValue(value)
	// A Goal first input may be echoed through a result-shaped or future event
	// envelope that has no top-level threadId. Redact every userMessage payload
	// before raw persistence rather than guessing that an unknown shape is safe.
	redactUserMessageValues(value)
	root, _ := value.(map[string]any)
	if root == nil {
		encoded, _ := json.Marshal(value)
		return encoded
	}
	params, _ := root["params"].(map[string]any)
	threadID, _ := params["threadId"].(string)
	if _, goal := t.goalThreads[threadID]; goal {
		if item, _ := params["item"].(map[string]any); item != nil {
			if itemType, _ := item["type"].(string); itemType == "userMessage" {
				redactTextFields(item)
			}
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return encoded
}

func redactUserMessageValues(value any) {
	switch typed := value.(type) {
	case map[string]any:
		if itemType, _ := typed["type"].(string); itemType == "userMessage" {
			redactTextFields(typed)
			return
		}
		for _, child := range typed {
			redactUserMessageValues(child)
		}
	case []any:
		for _, child := range typed {
			redactUserMessageValues(child)
		}
	}
}

// redactGoalObjective preserves protocol shape while ensuring a thread goal
// cannot enter the local debug evidence. It intentionally redacts every JSON
// field named objective: a harmless false positive is safer than a persisted
// user objective.
func redactGoalObjective(raw []byte) []byte {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return raw
	}
	redactObjectiveValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return encoded
}

func redactObjectiveValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "objective" {
				typed[key] = redactedText(child)
				continue
			}
			redactObjectiveValue(child)
		}
	case []any:
		for _, child := range typed {
			redactObjectiveValue(child)
		}
	}
}

func redactTextFields(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "text" {
				typed[key] = redactedText(child)
				continue
			}
			redactTextFields(child)
		}
	case []any:
		for _, child := range typed {
			redactTextFields(child)
		}
	}
}

func redactedText(value any) string {
	text, ok := value.(string)
	if !ok {
		return "[redacted]"
	}
	digest := sha256.Sum256([]byte(text))
	return fmt.Sprintf("[redacted sha256=%x bytes=%d]", digest, len([]byte(text)))
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
