package codexapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

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
