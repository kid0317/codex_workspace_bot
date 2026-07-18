package codexapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/observability"
)

func TestRouteAttemptExtractsAuthoritativeTurnCompletedUsage(t *testing.T) {
	runtime := &Runtime{attempts: make(map[*attempt]struct{})}
	var got TurnCompleted
	a := &attempt{threadID: "thread-1", turnID: "turn-1", done: make(chan error, 1), progress: make(chan struct{}, 1), onTurnCompleted: func(completed TurnCompleted) { got = completed }}
	params := json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","usage":{"inputTokens":100,"outputTokens":30,"cachedInputTokens":60,"reasoningOutputTokens":10,"totalTokens":130}}}`)
	runtime.routeAttempt(a, Event{Class: "notification", Method: "turn/completed", Params: params})
	if got.ThreadID != "thread-1" || got.TurnID != "turn-1" || got.Status != "completed" || got.Usage == nil {
		t.Fatalf("completed = %#v", got)
	}
	want := observability.Usage{InputTokens: 100, OutputTokens: 30, CachedInputTokens: 60, ReasoningOutputTokens: 10, TotalTokens: 130}
	if *got.Usage != want {
		t.Fatalf("usage = %#v, want %#v", *got.Usage, want)
	}
}

func TestDispatchRoutesThreadUsageSnapshotWithoutTurnID(t *testing.T) {
	runtime := &Runtime{attempts: make(map[*attempt]struct{})}
	observed := make(chan struct{}, 1)
	a := &attempt{threadID: "thread-1", turnID: "turn-1", done: make(chan error, 1), progress: make(chan struct{}, 1), observation: &observability.Attempt{}}
	// A zero-value Attempt is intentionally a no-op; route state is the
	// regression boundary here because snapshots do not include turnId.
	runtime.attempts[a] = struct{}{}
	params := json.RawMessage(`{"threadId":"thread-1","usage":{"inputTokens":100,"outputTokens":10,"totalTokens":110}}`)
	state, err := runtime.dispatch(Event{Class: "notification", Method: "thread/tokenUsage/updated", Params: params})
	if err != nil || state != "attempt_routed" {
		t.Fatalf("dispatch state=%q err=%v", state, err)
	}
	select {
	case <-a.progress:
		observed <- struct{}{}
	default:
	}
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("snapshot did not reach active attempt")
	}
}

func TestRouteAttemptUsesLastThreadSnapshotWhenCompletedUsageIsOmitted(t *testing.T) {
	runtime := &Runtime{attempts: make(map[*attempt]struct{})}
	var got TurnCompleted
	a := &attempt{threadID: "thread-1", turnID: "turn-1", done: make(chan error, 1), progress: make(chan struct{}, 1), onTurnCompleted: func(completed TurnCompleted) { got = completed }}
	runtime.routeAttempt(a, Event{Class: "notification", Method: "thread/tokenUsage/updated", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"inputTokens":120,"outputTokens":30,"cachedInputTokens":70,"reasoningOutputTokens":8,"totalTokens":150}}}`)})
	runtime.routeAttempt(a, Event{Class: "notification", Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`)})
	if got.Usage != nil || got.SnapshotUsage == nil || got.SnapshotUsage.TotalTokens != 150 {
		t.Fatalf("completed = %#v", got)
	}
}

func TestUsageSnapshotAcceptsExplicitZeroCounters(t *testing.T) {
	usage, ok := usageSnapshot(json.RawMessage(`{"threadId":"thread-1","tokenUsage":{"total":{"inputTokens":0,"outputTokens":0,"cachedInputTokens":0,"reasoningOutputTokens":0,"totalTokens":0}}}`))
	if !ok || usage != (observability.Usage{}) {
		t.Fatalf("usageSnapshot() = %#v, %t; want explicit zero usage", usage, ok)
	}
}

func TestBindAttemptDoesNotCarrySnapshotIntoNextTurn(t *testing.T) {
	runtime := &Runtime{attempts: make(map[*attempt]struct{})}
	previous := observability.Usage{InputTokens: 100, OutputTokens: 20, CachedInputTokens: 70, ReasoningOutputTokens: 5, TotalTokens: 120}
	a := &attempt{threadID: "thread-1", turnID: "turn-1", done: make(chan error, 1), progress: make(chan struct{}, 1), lastTokenUsage: &previous}
	if !runtime.bindAttempt(a, "turn-2") {
		t.Fatal("bindAttempt() = false")
	}
	if a.lastTokenUsage != nil {
		t.Fatalf("lastTokenUsage carried into a new turn: %#v", a.lastTokenUsage)
	}
}

func TestBindAttemptIgnoresEarlyEventForAnotherTurn(t *testing.T) {
	runtime := &Runtime{attempts: make(map[*attempt]struct{})}
	attempt := &attempt{threadID: "thread-new", done: make(chan error, 1), progress: make(chan struct{}, 1)}
	runtime.attempts[attempt] = struct{}{}
	params, _ := json.Marshal(map[string]any{"threadId": "thread-new", "turn": map[string]any{"id": "turn-old", "status": "completed"}})
	if state, err := runtime.dispatch(Event{Class: "notification", Method: "turn/completed", Params: params}); err != nil || state != "pending_turn_binding" {
		t.Fatalf("dispatch = %q, %v", state, err)
	}
	runtime.bindAttempt(attempt, "turn-new")
	select {
	case err := <-attempt.done:
		t.Fatalf("old turn completed incorrectly finished new attempt: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestRuntimeGoalKeepsAttemptAcrossContinuationTurns(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		defer server.Close()
		request, _ := ReadRequest(server)
		_ = WriteResponse(server, request.ID, map[string]any{"ok": true})
		request, _ = ReadRequest(server)
		if request.Method != "turn/start" {
			t.Errorf("first request = %s, want turn/start", request.Method)
			return
		}
		_ = WriteResponse(server, request.ID, map[string]any{"turn": map[string]any{"id": "turn-1"}})
		_ = writeInternalMessage(server, map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "thread-goal", "turn": map[string]any{"id": "turn-1", "status": "completed"}}})
		_ = writeInternalMessage(server, map[string]any{"jsonrpc": "2.0", "method": "turn/started", "params": map[string]any{"threadId": "thread-goal", "turn": map[string]any{"id": "turn-2"}}})
		_ = writeInternalMessage(server, map[string]any{"jsonrpc": "2.0", "method": "item/completed", "params": map[string]any{"threadId": "thread-goal", "turnId": "turn-2", "item": map[string]any{"id": "item-2", "type": "agentMessage", "phase": "final_answer", "text": "second visible output"}}})
		_ = writeInternalMessage(server, map[string]any{"jsonrpc": "2.0", "method": "thread/goal/updated", "params": map[string]any{"threadId": "thread-goal", "goal": map[string]any{"status": "complete"}}})
		_ = writeInternalMessage(server, map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "thread-goal", "turn": map[string]any{"id": "turn-2", "status": "completed"}}})
		time.Sleep(100 * time.Millisecond)
	}()
	runtime := NewRuntimeWithStarter(Config{RPCTimeout: time.Second, TurnTimeout: time.Second, IdleTimeout: time.Second, Grace: time.Second}, func() (io.ReadWriteCloser, error) { return client, nil })
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var items []string
	if _, err := runtime.StartGoal(context.Background(), "thread-goal", TurnStartParams{Input: []TextInput{{Type: "text", Text: "goal"}}, OnItem: func(item CompletedItem) bool {
		items = append(items, item.Text)
		return true
	}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(items, "|"); got != "second visible output" {
		t.Fatalf("items = %q", got)
	}
}

func TestRuntimeGoalRoutesContinuationItemWithoutTurnStarted(t *testing.T) {
	runtime := &Runtime{attempts: make(map[*attempt]struct{})}
	var items []string
	a := &attempt{
		threadID:   "thread-goal",
		turnID:     "turn-1",
		goal:       true,
		knownTurns: map[string]struct{}{"turn-1": {}},
		done:       make(chan error, 1),
		progress:   make(chan struct{}, 1),
		onItem: func(item CompletedItem) bool {
			items = append(items, item.Text)
			return true
		},
	}
	runtime.attempts[a] = struct{}{}
	firstCompleted, _ := json.Marshal(map[string]any{
		"threadId": "thread-goal",
		"turn":     map[string]any{"id": "turn-1", "status": "completed"},
	})
	if state, err := runtime.dispatch(Event{Class: "notification", Method: "turn/completed", Params: firstCompleted}); err != nil || state != "goal_routed" {
		t.Fatalf("first completed dispatch = %q, %v", state, err)
	}
	continuationItem, _ := json.Marshal(map[string]any{
		"threadId": "thread-goal",
		"turnId":   "turn-2",
		"item": map[string]any{
			"id": "item-2", "type": "agentMessage", "phase": "final_answer", "text": "continuation output",
		},
	})
	if state, err := runtime.dispatch(Event{Class: "notification", Method: "item/completed", Params: continuationItem}); err != nil || state != "goal_continuation_bound" {
		t.Fatalf("continuation item dispatch = %q, %v", state, err)
	}
	if got := strings.Join(items, "|"); got != "continuation output" {
		t.Fatalf("items = %q", got)
	}
	a.mu.Lock()
	current := a.turnID
	a.mu.Unlock()
	if current != "turn-2" {
		t.Fatalf("current turn = %q, want turn-2", current)
	}
}

func TestRuntimeGoalDoesNotBindContinuationFromNonAgentItem(t *testing.T) {
	runtime := &Runtime{attempts: make(map[*attempt]struct{})}
	a := &attempt{
		threadID:   "thread-goal",
		turnID:     "turn-1",
		goal:       true,
		knownTurns: map[string]struct{}{"turn-1": {}},
		done:       make(chan error, 1),
		progress:   make(chan struct{}, 1),
	}
	runtime.attempts[a] = struct{}{}
	firstCompleted, _ := json.Marshal(map[string]any{
		"threadId": "thread-goal",
		"turn":     map[string]any{"id": "turn-1", "status": "completed"},
	})
	_, _ = runtime.dispatch(Event{Class: "notification", Method: "turn/completed", Params: firstCompleted})
	userItem, _ := json.Marshal(map[string]any{
		"threadId": "thread-goal",
		"turnId":   "turn-2",
		"item":     map[string]any{"id": "item-2", "type": "userMessage", "text": "not output"},
	})
	if state, err := runtime.dispatch(Event{Class: "notification", Method: "item/completed", Params: userItem}); err != nil || state != "unrouted" {
		t.Fatalf("user item dispatch = %q, %v", state, err)
	}
	a.mu.Lock()
	current := a.turnID
	a.mu.Unlock()
	if current != "turn-1" {
		t.Fatalf("current turn = %q, want turn-1", current)
	}
}

func TestRuntimeGoalTerminalAfterCompletedTurnRejectsLateContinuation(t *testing.T) {
	runtime := &Runtime{attempts: make(map[*attempt]struct{})}
	var items []string
	a := &attempt{
		threadID:   "thread-goal",
		turnID:     "turn-1",
		goal:       true,
		knownTurns: map[string]struct{}{"turn-1": {}},
		done:       make(chan error, 1),
		progress:   make(chan struct{}, 1),
		onItem: func(item CompletedItem) bool {
			items = append(items, item.Text)
			return true
		},
	}
	runtime.attempts[a] = struct{}{}
	firstCompleted, _ := json.Marshal(map[string]any{
		"threadId": "thread-goal",
		"turn":     map[string]any{"id": "turn-1", "status": "completed"},
	})
	_, _ = runtime.dispatch(Event{Class: "notification", Method: "turn/completed", Params: firstCompleted})
	terminal, _ := json.Marshal(map[string]any{"threadId": "thread-goal", "goal": map[string]any{"status": "complete"}})
	_, _ = runtime.dispatch(Event{Class: "notification", Method: "thread/goal/updated", Params: terminal})
	select {
	case err := <-a.done:
		if err != nil {
			t.Fatalf("terminal error = %v", err)
		}
	default:
		t.Fatal("terminal update did not finish an already-completed Goal turn")
	}
	lateItem, _ := json.Marshal(map[string]any{
		"threadId": "thread-goal",
		"turnId":   "turn-2",
		"item": map[string]any{
			"id": "item-2", "type": "agentMessage", "phase": "final_answer", "text": "late output",
		},
	})
	if state, err := runtime.dispatch(Event{Class: "notification", Method: "item/completed", Params: lateItem}); err != nil || state != "unrouted" {
		t.Fatalf("late item dispatch = %q, %v", state, err)
	}
	if len(items) != 0 {
		t.Fatalf("late item was projected: %q", items)
	}
}

func TestPreparedGoalRoutesTerminalBeforeFirstTurnStarts(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		defer server.Close()
		request, _ := ReadRequest(server)
		_ = WriteResponse(server, request.ID, map[string]any{"ok": true})
		_ = writeInternalMessage(server, map[string]any{"jsonrpc": "2.0", "method": "thread/goal/updated", "params": map[string]any{"threadId": "thread-prepared", "goal": map[string]any{"status": "paused"}}})
		time.Sleep(100 * time.Millisecond)
	}()
	runtime := NewRuntimeWithStarter(Config{RPCTimeout: time.Second, TurnTimeout: time.Second, IdleTimeout: time.Second, Grace: time.Second}, func() (io.ReadWriteCloser, error) { return client, nil })
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	goal, err := runtime.PrepareGoal(context.Background(), "thread-prepared", TurnStartParams{Input: []TextInput{{Type: "text", Text: "goal"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer goal.Close()
	select {
	case err := <-goal.Done():
		if err == nil || !strings.Contains(err.Error(), "paused") {
			t.Fatalf("prepared goal terminal = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("prepared goal did not receive terminal update")
	}
}

func TestGoalBindDoesNotLetLateFirstResponseReplaceContinuation(t *testing.T) {
	runtime := &Runtime{attempts: make(map[*attempt]struct{})}
	a := &attempt{threadID: "thread-goal", goal: true, knownTurns: make(map[string]struct{}), done: make(chan error, 1), progress: make(chan struct{}, 1), turnChanged: make(chan struct{}, 1)}
	runtime.bindAttempt(a, "turn-1")
	runtime.bindAttempt(a, "turn-2")
	runtime.bindAttempt(a, "turn-1") // delayed response for the already-completed first turn
	a.mu.Lock()
	got := a.turnID
	a.mu.Unlock()
	if got != "turn-2" {
		t.Fatalf("current turn = %q, want continuation turn-2", got)
	}
}

func TestSnapshotExtractsItemIdentityFromObservedShapes(t *testing.T) {
	runtime := &Runtime{attempts: make(map[*attempt]struct{})}
	for _, raw := range []string{
		`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-direct"}`,
		`{"item":{"id":"item-nested","threadId":"thread-2","turnId":"turn-2"}}`,
	} {
		snapshot := runtime.snapshot(Event{Params: json.RawMessage(raw)})
		if snapshot["item_id"] == nil || snapshot["item_id"] == "" {
			t.Fatalf("snapshot for %s missing item_id: %#v", raw, snapshot)
		}
	}
}

func TestRedactGoalObjectiveNeverReturnsPlaintext(t *testing.T) {
	redacted := string(redactGoalObjective([]byte(`{"result":{"goal":{"objective":"sentinel-private-goal"}}}`)))
	if strings.Contains(redacted, "sentinel-private-goal") || !strings.Contains(redacted, "sha256=") {
		t.Fatalf("redacted raw = %s", redacted)
	}
}

func TestMissingTurnStartResponseClosesGenerationAndStartsReplacement(t *testing.T) {
	firstClient, firstServer := net.Pipe()
	secondClient, secondServer := net.Pipe()
	var starts atomic.Int32
	runtime := NewRuntimeWithStarter(Config{RPCTimeout: 20 * time.Millisecond, TurnTimeout: time.Second, IdleTimeout: time.Second, Grace: 20 * time.Millisecond}, func() (io.ReadWriteCloser, error) {
		if starts.Add(1) == 1 {
			return firstClient, nil
		}
		return secondClient, nil
	})
	defer runtime.Close()
	go func() {
		defer firstServer.Close()
		request, err := ReadRequest(firstServer)
		if err == nil {
			_ = WriteResponse(firstServer, request.ID, map[string]any{"ok": true})
		}
		_, _ = ReadRequest(firstServer)
	}()
	go func() {
		defer secondServer.Close()
		request, err := ReadRequest(secondServer)
		if err == nil {
			_ = WriteResponse(secondServer, request.ID, map[string]any{"ok": true})
		}
		time.Sleep(100 * time.Millisecond)
	}()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.StartTurn(context.Background(), "thread-1", TurnStartParams{Input: []TextInput{{Type: "text", Text: "x"}}}); err == nil {
		t.Fatal("StartTurn() succeeded without response")
	}
	deadline := time.After(time.Second)
	for runtime.Availability() != Ready || starts.Load() != 2 {
		select {
		case <-deadline:
			t.Fatalf("replacement did not initialize: starts=%d availability=%s", starts.Load(), runtime.Availability())
		case <-time.After(time.Millisecond):
		}
	}
}

func TestTimeoutWaitsForCompletedDuringInterruptGrace(t *testing.T) {
	client, server := net.Pipe()
	runtime := NewRuntimeWithStarter(Config{RPCTimeout: time.Second, TurnTimeout: 10 * time.Millisecond, IdleTimeout: time.Second, Grace: 100 * time.Millisecond}, func() (io.ReadWriteCloser, error) { return client, nil })
	defer runtime.Close()
	go func() {
		defer server.Close()
		request, _ := ReadRequest(server)
		_ = WriteResponse(server, request.ID, map[string]any{"ok": true})
		request, _ = ReadRequest(server)
		_ = WriteResponse(server, request.ID, map[string]any{"turn": map[string]any{"id": "turn-1"}})
		request, _ = ReadRequest(server)
		if request.Method == "turn/interrupt" {
			_ = writeInternalMessage(server, map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "status": "interrupted"}}})
		}
		time.Sleep(20 * time.Millisecond)
	}()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := runtime.StartTurn(context.Background(), "thread-1", TurnStartParams{Input: []TextInput{{Type: "text", Text: "x"}}})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) >= 100*time.Millisecond {
		t.Fatalf("StartTurn error=%v elapsed=%s", err, time.Since(start))
	}
}

func TestRuntimeRoutesToolCallWithAttemptContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 1)
	seenCancelled := make(chan bool, 1)
	runtime := &Runtime{attempts: make(map[*attempt]struct{})}
	a := &attempt{threadID: "thread-1", turnID: "turn-1", ctx: ctx, actionSlots: make(chan struct{}, 4), bound: make(chan struct{}), toolHandler: func(handlerCtx context.Context, _ ToolCall) (ToolResult, error) {
		started <- struct{}{}
		<-handlerCtx.Done()
		seenCancelled <- handlerCtx.Err() != nil
		return ToolResult{Success: false}, nil
	}}
	runtime.attempts[a] = struct{}{}
	errCh := make(chan error, 1)
	go func() {
		_, err := runtime.handleServerRequest(Event{Method: "item/tool/call", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","callId":"call-1","tool":"feishu.message_send_current_channel","arguments":{}}`)})
		errCh <- err
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("tool handler was not called")
	}
	select {
	case cancelled := <-seenCancelled:
		if !cancelled {
			t.Fatal("tool handler did not receive cancelled attempt context")
		}
	case <-time.After(time.Second):
		t.Fatal("tool handler did not receive cancellation")
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func writeInternalMessage(writer io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = writer.Write(append(raw, '\n'))
	return err
}
