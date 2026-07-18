package schedule

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestDispatcherMuxExecutesScriptAsynchronouslyThenDeliversResult(t *testing.T) {
	scripts := &fakeScriptRunService{executed: make(chan struct{}), completed: make(chan struct{})}
	delivery := &fakePromptDelivery{done: make(chan struct{}, 1)}
	dispatcher := DispatcherMux{Scripts: scripts, Delivery: delivery, ScriptSlots: make(chan struct{}, 1)}
	run := ClaimedRun{ID: "run-script", ClaimToken: "claim-script", Kind: TaskScript}
	if err := dispatcher.Dispatch(context.Background(), run); err != nil {
		t.Fatalf("Dispatch() error=%v", err)
	}
	select {
	case <-scripts.executed:
	case <-time.After(time.Second):
		t.Fatal("script executor was not called")
	}
	select {
	case <-scripts.completed:
	case <-time.After(time.Second):
		t.Fatal("script run was not completed")
	}
	select {
	case <-delivery.done:
	case <-time.After(time.Second):
		t.Fatal("script result was not delivered")
	}
	if scripts.markedRun != "run-script:claim-script" {
		t.Fatalf("marked=%q", scripts.markedRun)
	}
	if scripts.completion.State != RunSucceeded || scripts.completion.ErrorCode != "" {
		t.Fatalf("completion=%#v", scripts.completion)
	}
	if delivery.runID != "run-script" || !delivery.presentation.Succeeded || delivery.presentation.ScriptConsole != "```text\nhello\n```" || delivery.presentation.ExitCode != 0 {
		t.Fatalf("delivery=%#v", delivery)
	}
}

func TestSafeScriptConsoleUsesBoundedEscapedCodeFence(t *testing.T) {
	console, truncated := safeScriptConsole([]byte("# not a heading\n```\n![not an image](https://example.invalid)"))
	if truncated || console != "```text\n# not a heading\n``\u200b`\n![not an image](https://example.invalid)\n```" || len(console) == 0 {
		t.Fatalf("console=%q truncated=%t", console, truncated)
	}
	tooLarge := make([]byte, maxScriptConsoleCardBytes+1)
	for i := range tooLarge {
		tooLarge[i] = 'x'
	}
	console, truncated = safeScriptConsole(tooLarge)
	if !truncated || len(console) > maxScriptConsoleCardBytes+len("```text\n\n```") {
		t.Fatalf("console bytes=%d truncated=%t", len(console), truncated)
	}
}

func TestDispatcherMuxFailsCapacityLimitedScriptAndDeliversFailure(t *testing.T) {
	scripts := &fakeScriptRunService{}
	delivery := &fakePromptDelivery{}
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	dispatcher := DispatcherMux{Scripts: scripts, Delivery: delivery, ScriptSlots: slots}
	if err := dispatcher.Dispatch(context.Background(), ClaimedRun{ID: "run-capacity", ClaimToken: "claim-capacity", Kind: TaskScript}); err != nil {
		t.Fatalf("Dispatch() error=%v", err)
	}
	if scripts.failedBefore != "run-capacity:claim-capacity:failed_capacity" {
		t.Fatalf("failed=%q", scripts.failedBefore)
	}
	if delivery.runID != "run-capacity" || delivery.presentation.Succeeded || delivery.presentation.ErrorCode != "failed_capacity" {
		t.Fatalf("delivery=%#v", delivery)
	}
}

func TestDispatcherMuxRecoversExecutorPanicAsTerminalFailure(t *testing.T) {
	scripts := &fakeScriptRunService{panicExecute: true, failed: make(chan struct{}, 1)}
	delivery := &fakePromptDelivery{done: make(chan struct{}, 1)}
	dispatcher := DispatcherMux{Scripts: scripts, Delivery: delivery, ScriptSlots: make(chan struct{}, 1)}
	if err := dispatcher.Dispatch(context.Background(), ClaimedRun{ID: "run-panic", ClaimToken: "claim-panic", Kind: TaskScript}); err != nil {
		t.Fatalf("Dispatch() error=%v", err)
	}
	select {
	case <-scripts.failed:
	case <-time.After(time.Second):
		t.Fatal("panic did not finalize run")
	}
	select {
	case <-delivery.done:
	case <-time.After(time.Second):
		t.Fatal("panic result was not delivered")
	}
	if scripts.failedBefore != "run-panic:claim-panic:failed_executor_panic" || delivery.presentation.ErrorCode != "failed_executor_panic" {
		t.Fatalf("scripts=%#v delivery=%#v", scripts, delivery)
	}
}

func TestDispatcherMuxClassifiesSandboxTimeout(t *testing.T) {
	scripts := &fakeScriptRunService{result: ScriptExecutionResult{ExitCode: 124}, completed: make(chan struct{})}
	delivery := &fakePromptDelivery{done: make(chan struct{}, 1)}
	dispatcher := DispatcherMux{Scripts: scripts, Delivery: delivery, ScriptSlots: make(chan struct{}, 1)}
	if err := dispatcher.Dispatch(context.Background(), ClaimedRun{ID: "run-timeout", ClaimToken: "claim-timeout", Kind: TaskScript}); err != nil {
		t.Fatalf("Dispatch() error=%v", err)
	}
	select {
	case <-scripts.completed:
	case <-time.After(time.Second):
		t.Fatal("timeout run was not completed")
	}
	select {
	case <-delivery.done:
	case <-time.After(time.Second):
		t.Fatal("timeout result was not delivered")
	}
	if scripts.completion.State != RunFailed || scripts.completion.ErrorCode != "script_timeout" || delivery.presentation.ErrorCode != "script_timeout" {
		t.Fatalf("completion=%#v delivery=%#v", scripts.completion, delivery)
	}
}

func TestDispatcherMuxClassifiesScriptExecutorFailure(t *testing.T) {
	scripts := &fakeScriptRunService{executeErr: fmt.Errorf("command process failed"), completed: make(chan struct{})}
	delivery := &fakePromptDelivery{done: make(chan struct{}, 1)}
	dispatcher := DispatcherMux{Scripts: scripts, Delivery: delivery, ScriptSlots: make(chan struct{}, 1)}
	if err := dispatcher.Dispatch(context.Background(), ClaimedRun{ID: "run-untrusted", ClaimToken: "claim-untrusted", Kind: TaskScript}); err != nil {
		t.Fatalf("Dispatch() error=%v", err)
	}
	select {
	case <-scripts.completed:
	case <-time.After(time.Second):
		t.Fatal("failed run was not completed")
	}
	select {
	case <-delivery.done:
	case <-time.After(time.Second):
		t.Fatal("failed result was not delivered")
	}
	if scripts.completion.State != RunFailed || scripts.completion.ErrorCode != "failed_script_executor" || delivery.presentation.ErrorCode != "failed_script_executor" {
		t.Fatalf("completion=%#v delivery=%#v", scripts.completion, delivery)
	}
}

type fakeScriptRunService struct {
	mu                      sync.Mutex
	markedRun, failedBefore string
	completion              RunCompletion
	executed, completed     chan struct{}
	failed                  chan struct{}
	panicExecute            bool
	executeErr              error
	result                  ScriptExecutionResult
}

func (s *fakeScriptRunService) MarkRunRunning(_ context.Context, runID, claimToken string) error {
	s.mu.Lock()
	s.markedRun = runID + ":" + claimToken
	s.mu.Unlock()
	return nil
}
func (s *fakeScriptRunService) Execute(context.Context, ClaimedRun) (ScriptExecutionResult, error) {
	if s.panicExecute {
		panic("test executor panic")
	}
	if s.executed != nil {
		s.executed <- struct{}{}
	}
	if s.executeErr != nil {
		return ScriptExecutionResult{}, s.executeErr
	}
	if s.result.ExitCode != 0 || len(s.result.Output) != 0 || len(s.result.Stdout) != 0 || len(s.result.Stderr) != 0 || s.result.Truncated {
		return s.result, nil
	}
	return ScriptExecutionResult{Output: []byte("hello")}, nil
}
func (*fakeScriptRunService) Metadata(ScriptExecutionResult) (ScriptRunMetadata, error) {
	return ScriptRunMetadata{StdoutHMAC: "stdout", StderrHMAC: "stderr"}, nil
}
func (s *fakeScriptRunService) CompleteScriptRun(_ context.Context, completion RunCompletion, _ ScriptRunMetadata) error {
	s.mu.Lock()
	s.completion = completion
	s.mu.Unlock()
	if s.completed != nil {
		s.completed <- struct{}{}
	}
	return nil
}
func (s *fakeScriptRunService) FailBeforeExecution(_ context.Context, runID, claimToken, code string) error {
	s.mu.Lock()
	s.failedBefore = runID + ":" + claimToken + ":" + code
	s.mu.Unlock()
	if s.failed != nil {
		s.failed <- struct{}{}
	}
	return nil
}
