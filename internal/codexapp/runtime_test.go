package codexapp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/catalog"
	"github.com/kid0317/codex-workspace-bot/internal/codexapp"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

type memoryThreadStore struct {
	mu      sync.Mutex
	thread  string
	toolset string
}

type catalogThreadStore struct {
	memoryThreadStore
	state     catalog.Upgrade
	advanced  bool
	completed bool
}

func (s *catalogThreadStore) PrepareCatalogUpgrade(context.Context, string, string) (catalog.Upgrade, error) {
	return s.state, nil
}
func (s *catalogThreadStore) AdvanceCatalogUpgradeAfterArchive(_ context.Context, _ string, archived string) (bool, error) {
	if archived != s.state.FromThreadID {
		return false, nil
	}
	s.advanced = true
	s.state.State = catalog.StartPending
	s.memoryThreadStore.thread = ""
	return true, nil
}
func (s *catalogThreadStore) CompleteCatalogUpgrade(_ context.Context, _ string, target, thread string) (bool, error) {
	if s.state.State != catalog.StartPending || target != s.state.Target {
		return false, nil
	}
	s.completed = true
	s.memoryThreadStore.thread = thread
	s.state.CurrentThreadID, s.state.CurrentVersion, s.state.State = thread, target, catalog.Stable
	return true, nil
}

type fakeAttachmentResolver struct{ inputs []codexapp.TextInput }

func (r fakeAttachmentResolver) Prepare(context.Context, worker.Batch) ([]codexapp.TextInput, error) {
	return r.inputs, nil
}

func (s *memoryThreadStore) GetChatGroupThread(context.Context, string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.thread, nil
}
func (s *memoryThreadStore) SetThreadIfExpected(_ context.Context, _ string, expected, replacement string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.thread != expected {
		return false, nil
	}
	s.thread = replacement
	return true, nil
}
func (s *memoryThreadStore) GetChatGroupToolset(context.Context, string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toolset, nil
}
func (s *memoryThreadStore) SetChatGroupToolset(_ context.Context, _ string, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolset = version
	return nil
}

func TestProcessorWaitsForTurnCompletedAndPersistsThread(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	go fakeAppServer(t, serverSide)
	runtime := codexapp.NewRuntimeWithStarter(codexapp.Config{RPCTimeout: time.Second, TurnTimeout: time.Second, IdleTimeout: time.Second, Grace: time.Second}, func() (io.ReadWriteCloser, error) { return clientSide, nil })
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	store := &memoryThreadStore{}
	batch := worker.Batch{Runtime: worker.AppRuntime{WorkspaceDir: "/tmp/workspace", Model: "test", Effort: "medium"}, Messages: []worker.Message{{ID: "m1", ChatGroupID: "group-1", Query: "one"}, {ID: "m2", ChatGroupID: "group-1", Query: "two"}}}
	if _, err := (codexapp.Processor{Runtime: runtime, Store: store}).Process(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if thread, _ := store.GetChatGroupThread(context.Background(), "group-1"); thread != "thread-1" {
		t.Fatalf("thread = %q", thread)
	}
}

func TestProcessorUsesOneOrderedTextInputAndExplicitRuntimeFields(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	go func() {
		defer serverSide.Close()
		request, err := codexapp.ReadRequest(serverSide)
		if err != nil {
			return
		}
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"ok": true})
		request, err = codexapp.ReadRequest(serverSide)
		if err != nil {
			return
		}
		var threadParams struct{ CWD, Model, ApprovalPolicy, Sandbox string }
		if err := json.Unmarshal(request.Params, &threadParams); err != nil {
			t.Errorf("thread params: %v", err)
			return
		}
		if request.Method != "thread/start" || threadParams.CWD != "/tmp/workspace" || threadParams.Model != "model-1" || threadParams.ApprovalPolicy != "never" || threadParams.Sandbox != "danger-full-access" {
			t.Errorf("thread request = %s %#v", request.Method, threadParams)
		}
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"thread": map[string]any{"id": "thread-input"}})
		request, err = codexapp.ReadRequest(serverSide)
		if err != nil {
			return
		}
		var turnParams struct {
			CWD, Model, Effort string
			Input              []struct{ Type, Text string }
		}
		if err := json.Unmarshal(request.Params, &turnParams); err != nil {
			t.Errorf("turn params: %v", err)
			return
		}
		wantText := "<now timezone=\"Asia/Shanghai\">2026-07-13T16:20:00+08:00</now>\nfirst\n<now timezone=\"Asia/Shanghai\">2026-07-13T16:21:00+08:00</now>\nsecond"
		if request.Method != "turn/start" || turnParams.CWD != "/tmp/workspace" || turnParams.Model != "model-1" || turnParams.Effort != "medium" || len(turnParams.Input) != 1 || turnParams.Input[0].Type != "text" || turnParams.Input[0].Text != wantText {
			t.Errorf("turn request = %s %#v", request.Method, turnParams)
		}
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"turn": map[string]any{"id": "turn-input"}})
		_ = writeMessage(serverSide, map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "thread-input", "turn": map[string]any{"id": "turn-input", "status": "completed"}}})
		time.Sleep(10 * time.Millisecond)
	}()
	runtime := codexapp.NewRuntimeWithStarter(codexapp.Config{RPCTimeout: time.Second, TurnTimeout: time.Second, IdleTimeout: time.Second, Grace: time.Second}, func() (io.ReadWriteCloser, error) { return clientSide, nil })
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	store := &memoryThreadStore{}
	batch := worker.Batch{Runtime: worker.AppRuntime{WorkspaceDir: "/tmp/workspace", Model: "model-1", Effort: "medium"}, Messages: []worker.Message{{ID: "m1", ChatGroupID: "group-input", Query: "first", ReceivedAt: time.Date(2026, 7, 13, 8, 20, 0, 0, time.UTC)}, {ID: "m2", ChatGroupID: "group-input", Query: "second", ReceivedAt: time.Date(2026, 7, 13, 8, 21, 0, 0, time.UTC)}}}
	if _, err := (codexapp.Processor{Runtime: runtime, Store: store}).Process(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
}

func TestProcessorGoalResumesSetsGoalAndStartsObjectiveTurn(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	go func() {
		defer serverSide.Close()
		request, _ := codexapp.ReadRequest(serverSide)
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"ok": true})
		request, _ = codexapp.ReadRequest(serverSide)
		if request.Method != "thread/resume" {
			t.Errorf("first goal request = %s", request.Method)
			return
		}
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"thread": map[string]any{"id": "thread-goal"}})
		request, _ = codexapp.ReadRequest(serverSide)
		if request.Method != "thread/goal/set" || !strings.Contains(string(request.Params), "sentinel-goal") {
			t.Errorf("goal request = %s %s", request.Method, request.Params)
			return
		}
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"ok": true})
		request, _ = codexapp.ReadRequest(serverSide)
		var turnParams struct{ Input []struct{ Type, Text string } }
		if request.Method != "turn/start" || json.Unmarshal(request.Params, &turnParams) != nil || len(turnParams.Input) != 1 || turnParams.Input[0].Text != "sentinel-goal" {
			t.Errorf("goal turn request = %s %s", request.Method, request.Params)
			return
		}
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"turn": map[string]any{"id": "turn-goal"}})
		_ = writeMessage(serverSide, map[string]any{"jsonrpc": "2.0", "method": "thread/goal/updated", "params": map[string]any{"threadId": "thread-goal", "goal": map[string]any{"status": "complete"}}})
		_ = writeMessage(serverSide, map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "thread-goal", "turn": map[string]any{"id": "turn-goal", "status": "completed"}}})
		time.Sleep(20 * time.Millisecond)
	}()
	runtime := codexapp.NewRuntimeWithStarter(codexapp.Config{RPCTimeout: time.Second}, func() (io.ReadWriteCloser, error) { return clientSide, nil })
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	processor := codexapp.Processor{Runtime: runtime, Store: &memoryThreadStore{thread: "thread-goal"}}
	if _, err := processor.Process(context.Background(), worker.Batch{Runtime: worker.AppRuntime{WorkspaceDir: "/tmp/workspace"}, Goal: true, Messages: []worker.Message{{ID: "m-goal", ChatGroupID: "group-goal", Query: "sentinel-goal"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestProcessorRegistersFeishuDynamicToolNamespaceOnNewThread(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	go func() {
		defer serverSide.Close()
		request, _ := codexapp.ReadRequest(serverSide)
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]bool{"ok": true})
		request, _ = codexapp.ReadRequest(serverSide)
		var params struct {
			DynamicTools []struct{ Namespace, Name, Description string }
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || len(params.DynamicTools) != 4 {
			t.Errorf("dynamic tools = %#v err=%v", params.DynamicTools, err)
			return
		}
		expectedNames := map[string]bool{
			"message_send_current_channel":         true,
			"file_upload_and_send_current_channel": true,
			"doc_create_and_announce":              true,
			"doc_read":                             true,
		}
		for _, tool := range params.DynamicTools {
			if tool.Namespace != "feishu" || !expectedNames[tool.Name] {
				t.Errorf("tool=%#v", tool)
			}
			if (tool.Name == "file_upload_and_send_current_channel" || tool.Name == "doc_create_and_announce") && !strings.Contains(strings.ToLower(tool.Description), "absolute path") {
				t.Errorf("tool %q description must require an absolute path: %q", tool.Name, tool.Description)
			}
			delete(expectedNames, tool.Name)
		}
		if len(expectedNames) != 0 {
			t.Errorf("missing dynamic tool names: %#v", expectedNames)
		}
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"thread": map[string]any{"id": "thread-dynamic"}})
		request, _ = codexapp.ReadRequest(serverSide)
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"turn": map[string]any{"id": "turn-dynamic"}})
		_ = writeMessage(serverSide, map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "thread-dynamic", "turn": map[string]any{"id": "turn-dynamic", "status": "completed"}}})
		time.Sleep(10 * time.Millisecond)
	}()
	runtime := codexapp.NewRuntimeWithStarter(codexapp.Config{RPCTimeout: time.Second, TurnTimeout: time.Second, IdleTimeout: time.Second, Grace: time.Second}, func() (io.ReadWriteCloser, error) { return clientSide, nil })
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	processor := codexapp.Processor{Runtime: runtime, Store: &memoryThreadStore{}, ToolHandlers: func(worker.Batch) codexapp.ToolHandler {
		return func(context.Context, codexapp.ToolCall) (codexapp.ToolResult, error) {
			return codexapp.ToolResult{Success: true}, nil
		}
	}}
	if _, err := processor.Process(context.Background(), worker.Batch{Runtime: worker.AppRuntime{WorkspaceDir: "/tmp/workspace"}, Messages: []worker.Message{{ID: "m-1", ChatGroupID: "group-dynamic", Query: "hello"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestProcessorNeverResumesOldThreadWhileCatalogUpgradeIsPending(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	go func() {
		defer serverSide.Close()
		request, _ := codexapp.ReadRequest(serverSide)
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]bool{"ok": true})
		request, _ = codexapp.ReadRequest(serverSide)
		if request.Method != "thread/archive" {
			t.Errorf("first catalog request = %s, want thread/archive", request.Method)
			return
		}
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]bool{"ok": true})
		request, _ = codexapp.ReadRequest(serverSide)
		var start struct {
			DynamicTools []codexapp.DynamicTool `json:"dynamicTools"`
		}
		if request.Method != "thread/start" || json.Unmarshal(request.Params, &start) != nil || len(start.DynamicTools) != 7 {
			t.Errorf("catalog start request=%s tools=%d", request.Method, len(start.DynamicTools))
			return
		}
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"thread": map[string]any{"id": "thread-s06"}})
		request, _ = codexapp.ReadRequest(serverSide)
		if request.Method != "turn/start" {
			t.Errorf("request after catalog start=%s", request.Method)
			return
		}
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"turn": map[string]any{"id": "turn-s06"}})
		_ = writeMessage(serverSide, map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "thread-s06", "turn": map[string]any{"id": "turn-s06", "status": "completed"}}})
		// The runtime receives protocol frames asynchronously; keep the test
		// transport open long enough for the terminal notification to route,
		// matching the existing dynamic-tool fixture above.
		time.Sleep(10 * time.Millisecond)
	}()
	runtime := codexapp.NewRuntimeWithStarter(codexapp.Config{RPCTimeout: time.Second, TurnTimeout: time.Second, IdleTimeout: time.Second, Grace: time.Second}, func() (io.ReadWriteCloser, error) { return clientSide, nil })
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	store := &catalogThreadStore{memoryThreadStore: memoryThreadStore{thread: "thread-old"}, state: catalog.Upgrade{CurrentThreadID: "thread-old", CurrentVersion: "s05-feishu-v2", State: catalog.ArchivePending, FromThreadID: "thread-old", Target: codexapp.S06ToolCatalogVersion}}
	processor := codexapp.Processor{Runtime: runtime, Store: store, ToolCatalogVersion: codexapp.S06ToolCatalogVersion, ToolHandlers: func(worker.Batch) codexapp.ToolHandler {
		return func(context.Context, codexapp.ToolCall) (codexapp.ToolResult, error) {
			return codexapp.ToolResult{}, nil
		}
	}}
	if _, err := processor.Process(context.Background(), worker.Batch{Runtime: worker.AppRuntime{WorkspaceDir: "/tmp/workspace"}, Messages: []worker.Message{{ID: "m-s06", ChatGroupID: "group-1", Query: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	if !store.advanced || !store.completed || store.thread != "thread-s06" {
		t.Fatalf("catalog state=%#v advanced=%t completed=%t thread=%q", store.state, store.advanced, store.completed, store.thread)
	}
}

func TestProcessorScheduledPromptReturnsFinalAnswerOnlyInProcessResult(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	go func() {
		defer serverSide.Close()
		request, _ := codexapp.ReadRequest(serverSide)
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]bool{"ok": true})
		request, _ = codexapp.ReadRequest(serverSide)
		if request.Method != "thread/resume" {
			t.Errorf("request=%s want thread/resume", request.Method)
			return
		}
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]bool{"ok": true})
		request, _ = codexapp.ReadRequest(serverSide)
		if request.Method != "turn/start" {
			t.Errorf("request=%s want turn/start", request.Method)
			return
		}
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"turn": map[string]any{"id": "turn-scheduled"}})
		// Match the App Server response-before-notification boundary used by the
		// existing runtime fixtures so the turn ID can bind before test events.
		time.Sleep(10 * time.Millisecond)
		_ = writeMessage(serverSide, map[string]any{"jsonrpc": "2.0", "method": "item/completed", "params": map[string]any{"threadId": "thread-scheduled", "turnId": "turn-scheduled", "item": map[string]any{"id": "item-final", "type": "agentMessage", "phase": "final_answer", "text": "scheduled final"}}})
		_ = writeMessage(serverSide, map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "thread-scheduled", "turn": map[string]any{"id": "turn-scheduled", "status": "completed"}}})
		time.Sleep(10 * time.Millisecond)
	}()
	runtime := codexapp.NewRuntimeWithStarter(codexapp.Config{RPCTimeout: time.Second, TurnTimeout: time.Second, IdleTimeout: time.Second, Grace: time.Second}, func() (io.ReadWriteCloser, error) { return clientSide, nil })
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := (codexapp.Processor{Runtime: runtime, Store: &memoryThreadStore{thread: "thread-scheduled"}}).Process(context.Background(), worker.Batch{Runtime: worker.AppRuntime{WorkspaceDir: "/tmp/workspace"}, ScheduledTaskRunID: "run-1", ScheduledClaimToken: "claim-1", Messages: []worker.Message{{ID: "m-scheduled", ChatGroupID: "group-1", Query: "reminder", ScheduledTaskRunID: "run-1", ScheduledClaimToken: "claim-1"}}})
	if err != nil || result.FinalText != "scheduled final" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestProcessorAddsResolvedLocalImageAndFileManifestToAttachmentTurn(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	go func() {
		defer serverSide.Close()
		request, _ := codexapp.ReadRequest(serverSide)
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"ok": true})
		request, _ = codexapp.ReadRequest(serverSide)
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"thread": map[string]any{"id": "thread-attachment"}})
		request, _ = codexapp.ReadRequest(serverSide)
		var turnParams struct {
			Input []struct {
				Type, Text, Path, Detail string
			}
		}
		if err := json.Unmarshal(request.Params, &turnParams); err != nil {
			t.Errorf("turn params: %v", err)
			return
		}
		if len(turnParams.Input) != 3 || turnParams.Input[0].Text != "describe these" || turnParams.Input[1].Type != "localImage" || turnParams.Input[1].Path != "/workspace/attachment.png" || turnParams.Input[2].Type != "text" || turnParams.Input[2].Text != "File manifest: report.pdf" {
			t.Errorf("attachment inputs = %#v", turnParams.Input)
		}
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"turn": map[string]any{"id": "turn-attachment"}})
		_ = writeMessage(serverSide, map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "thread-attachment", "turn": map[string]any{"id": "turn-attachment", "status": "completed"}}})
		time.Sleep(10 * time.Millisecond)
	}()
	runtime := codexapp.NewRuntimeWithStarter(codexapp.Config{RPCTimeout: time.Second, TurnTimeout: time.Second, IdleTimeout: time.Second, Grace: time.Second}, func() (io.ReadWriteCloser, error) { return clientSide, nil })
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	batch := worker.Batch{Runtime: worker.AppRuntime{WorkspaceDir: "/workspace"}, Messages: []worker.Message{{ID: "m-attachment", ChatGroupID: "group-attachment", Query: "describe these", HasRequiredAttachment: true}}}
	resolver := fakeAttachmentResolver{inputs: []codexapp.TextInput{{Type: "localImage", Path: "/workspace/attachment.png", Detail: "auto"}, {Type: "text", Text: "File manifest: report.pdf"}}}
	if _, err := (codexapp.Processor{Runtime: runtime, Store: &memoryThreadStore{}, Attachments: resolver}).Process(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimePublishesOnlyCompletedAgentMessageItemsToBoundTurn(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	go func() {
		request, _ := codexapp.ReadRequest(serverSide)
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"ok": true})
		request, _ = codexapp.ReadRequest(serverSide)
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"turn": map[string]any{"id": "turn-present"}})
		time.Sleep(10 * time.Millisecond)
		_ = writeMessage(serverSide, map[string]any{"jsonrpc": "2.0", "method": "item/completed", "params": map[string]any{"threadId": "thread-present", "turnId": "turn-present", "item": map[string]any{"id": "item-1", "type": "agentMessage", "phase": "commentary", "text": "visible"}}})
		_ = writeMessage(serverSide, map[string]any{"jsonrpc": "2.0", "method": "item/completed", "params": map[string]any{"threadId": "thread-present", "turnId": "turn-present", "item": map[string]any{"id": "item-2", "type": "reasoning", "phase": "commentary", "text": "hidden"}}})
		_ = writeMessage(serverSide, map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "thread-present", "turn": map[string]any{"id": "turn-present", "status": "completed"}}})
	}()
	runtime := codexapp.NewRuntimeWithStarter(codexapp.Config{RPCTimeout: time.Second, TurnTimeout: time.Second, IdleTimeout: time.Second, Grace: time.Second}, func() (io.ReadWriteCloser, error) { return clientSide, nil })
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var got []codexapp.CompletedItem
	var turnID string
	_, err := runtime.StartTurn(context.Background(), "thread-present", codexapp.TurnStartParams{OnTurnStarted: func(id string) { turnID = id }, OnItem: func(item codexapp.CompletedItem) bool { got = append(got, item); return true }})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "item-1" || got[0].Text != "visible" || got[0].Phase != "commentary" {
		t.Fatalf("published items = %#v", got)
	}
	if turnID != "turn-present" {
		t.Fatalf("turn id = %q", turnID)
	}
}

func TestProcessorOmitsEmptyTextInputForAttachmentOnlyMessage(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	go func() {
		defer serverSide.Close()
		request, _ := codexapp.ReadRequest(serverSide)
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]bool{"ok": true})
		request, _ = codexapp.ReadRequest(serverSide)
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"thread": map[string]any{"id": "thread-image-only"}})
		request, _ = codexapp.ReadRequest(serverSide)
		var turnParams struct {
			Input []struct {
				Type, Text, Path string
			}
		}
		if err := json.Unmarshal(request.Params, &turnParams); err != nil {
			t.Errorf("turn params: %v", err)
			return
		}
		if len(turnParams.Input) != 1 || turnParams.Input[0].Type != "localImage" || turnParams.Input[0].Text != "" || turnParams.Input[0].Path != "/workspace/attachment.png" {
			t.Errorf("attachment-only inputs = %#v", turnParams.Input)
		}
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"turn": map[string]any{"id": "turn-image-only"}})
		_ = writeMessage(serverSide, map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "thread-image-only", "turn": map[string]any{"id": "turn-image-only", "status": "completed"}}})
		time.Sleep(10 * time.Millisecond)
	}()
	runtime := codexapp.NewRuntimeWithStarter(codexapp.Config{RPCTimeout: time.Second, TurnTimeout: time.Second, IdleTimeout: time.Second, Grace: time.Second}, func() (io.ReadWriteCloser, error) { return clientSide, nil })
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	batch := worker.Batch{Runtime: worker.AppRuntime{WorkspaceDir: "/workspace"}, Messages: []worker.Message{{ID: "m-image-only", ChatGroupID: "group-image-only", HasRequiredAttachment: true}}}
	resolver := fakeAttachmentResolver{inputs: []codexapp.TextInput{{Type: "localImage", Path: "/workspace/attachment.png", Detail: "auto"}}}
	if _, err := (codexapp.Processor{Runtime: runtime, Store: &memoryThreadStore{}, Attachments: resolver}).Process(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeRoutesBoundDynamicToolCallToTurnHandler(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	handled := make(chan codexapp.ToolCall, 1)
	go func() {
		defer serverSide.Close()
		request, _ := codexapp.ReadRequest(serverSide)
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"ok": true})
		request, _ = codexapp.ReadRequest(serverSide)
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"turn": map[string]any{"id": "turn-tool"}})
		_ = writeMessage(serverSide, map[string]any{"jsonrpc": "2.0", "id": "tool-request-1", "method": "item/tool/call", "params": map[string]any{"threadId": "thread-tool", "turnId": "turn-tool", "callId": "call-1", "tool": "feishu.message_send_current_channel", "arguments": map[string]any{"text": "hello"}}})
		line, err := bufio.NewReader(serverSide).ReadBytes('\n')
		if err != nil {
			t.Errorf("tool response: %v", err)
			return
		}
		var response struct {
			ID     string `json:"id"`
			Result struct {
				Success bool `json:"success"`
			} `json:"result"`
		}
		if err := json.Unmarshal(line, &response); err != nil || response.ID != "tool-request-1" || !response.Result.Success {
			t.Errorf("tool response=%s err=%v", line, err)
		}
		_ = writeMessage(serverSide, map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "thread-tool", "turn": map[string]any{"id": "turn-tool", "status": "completed"}}})
	}()
	runtime := codexapp.NewRuntimeWithStarter(codexapp.Config{RPCTimeout: time.Second, TurnTimeout: time.Second, IdleTimeout: time.Second, Grace: time.Second}, func() (io.ReadWriteCloser, error) { return clientSide, nil })
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := runtime.StartTurn(context.Background(), "thread-tool", codexapp.TurnStartParams{ToolHandler: func(_ context.Context, call codexapp.ToolCall) (codexapp.ToolResult, error) {
		handled <- call
		return codexapp.ToolResult{Success: true, ContentItems: []codexapp.ToolContentItem{{Type: "inputText", Text: "sent"}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case call := <-handled:
		if call.CallID != "call-1" || call.ThreadID != "thread-tool" || call.TurnID != "turn-tool" {
			t.Fatalf("call=%#v", call)
		}
	case <-time.After(time.Second):
		t.Fatal("tool handler was not called")
	}
}

func TestRuntimeReportsPresentationBackpressureAndInterruptsBoundTurn(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	interrupted := make(chan struct{}, 1)
	go func() {
		request, _ := codexapp.ReadRequest(serverSide)
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"ok": true})
		request, _ = codexapp.ReadRequest(serverSide)
		_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{"turn": map[string]any{"id": "turn-pressure"}})
		_ = writeMessage(serverSide, map[string]any{"jsonrpc": "2.0", "method": "item/completed", "params": map[string]any{"threadId": "thread-pressure", "turnId": "turn-pressure", "item": map[string]any{"id": "item-pressure", "type": "agentMessage", "phase": "commentary", "text": "visible"}}})
		request, err := codexapp.ReadRequest(serverSide)
		if err == nil && request.Method == "turn/interrupt" {
			interrupted <- struct{}{}
			_ = codexapp.WriteResponse(serverSide, request.ID, map[string]any{})
		}
	}()
	runtime := codexapp.NewRuntimeWithStarter(codexapp.Config{RPCTimeout: time.Second, TurnTimeout: time.Second, IdleTimeout: time.Second, Grace: time.Second}, func() (io.ReadWriteCloser, error) { return clientSide, nil })
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := runtime.StartTurn(context.Background(), "thread-pressure", codexapp.TurnStartParams{OnItem: func(codexapp.CompletedItem) bool { return false }})
	if err == nil || err.Error() != "presentation_backpressure" {
		t.Fatalf("StartTurn error = %v", err)
	}
	select {
	case <-interrupted:
	case <-time.After(time.Second):
		t.Fatal("turn/interrupt was not sent")
	}
}

func TestProcessorDurationStartsWhenTurnStartIsAccepted(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	go fakeAppServer(t, serverSide)
	times := []time.Time{time.Unix(100, 0), time.Unix(100, 25_000_000)}
	next := 0
	runtime := codexapp.NewRuntimeWithStarter(codexapp.Config{
		RPCTimeout: time.Second, TurnTimeout: time.Second, IdleTimeout: time.Second, Grace: time.Second,
		Now: func() time.Time { value := times[next]; next++; return value },
	}, func() (io.ReadWriteCloser, error) { return clientSide, nil })
	defer runtime.Close()
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	batch := worker.Batch{Runtime: worker.AppRuntime{WorkspaceDir: "/tmp/workspace", Model: "test", Effort: "medium"}, Messages: []worker.Message{{ID: "m1", ChatGroupID: "group-duration", Query: "one"}}}
	result, err := (codexapp.Processor{Runtime: runtime, Store: &memoryThreadStore{}}).Process(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if result.DurationMS != 25 {
		t.Fatalf("duration_ms = %d, want only the accepted turn duration", result.DurationMS)
	}
}

func fakeAppServer(t *testing.T, conn net.Conn) {
	t.Helper()
	defer conn.Close()
	req, err := codexapp.ReadRequest(conn)
	if err != nil {
		return
	}
	if req.Method != "initialize" {
		t.Errorf("first method = %s", req.Method)
		return
	}
	_ = codexapp.WriteResponse(conn, req.ID, map[string]any{"codexHome": "/tmp"})
	req, err = codexapp.ReadRequest(conn)
	if err != nil {
		return
	}
	if req.Method != "thread/start" {
		t.Errorf("second method = %s", req.Method)
		return
	}
	_ = codexapp.WriteResponse(conn, req.ID, map[string]any{"thread": map[string]any{"id": "thread-1"}})
	req, err = codexapp.ReadRequest(conn)
	if err != nil {
		return
	}
	if req.Method != "turn/start" {
		t.Errorf("third method = %s", req.Method)
		return
	}
	_ = writeMessage(conn, map[string]any{"jsonrpc": "2.0", "method": "turn/started", "params": map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1"}}})
	_ = codexapp.WriteResponse(conn, req.ID, map[string]any{"turn": map[string]any{"id": "turn-1"}})
	_ = writeMessage(conn, map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "status": "completed"}}})
	time.Sleep(20 * time.Millisecond)
}

func writeMessage(conn net.Conn, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(body, '\n'))
	return err
}
