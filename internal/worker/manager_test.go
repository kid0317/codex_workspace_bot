package worker_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

type fakeOutput struct {
	mu                sync.Mutex
	created           []worker.Batch
	updated           []string
	texts             int
	textBody          []string
	updateErr         error
	companionOutcomes []worker.CompanionSendResult
	zones             []struct {
		final, progress, summary string
		closed                   bool
	}
}

type fakeLifecycle struct {
	mu                    sync.Mutex
	processing            []string
	completed             []string
	durations             []int64
	failureCodes          []string
	companionFailureCodes []string
}

type fakeGoalStopper struct {
	called chan string
}

func (s *fakeGoalStopper) RequestGoalStop(_ context.Context, batchID string) error {
	s.called <- batchID
	return nil
}

func (f *fakeLifecycle) MarkProcessing(_ context.Context, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.processing = append(f.processing, ids...)
	return nil
}

func (f *fakeLifecycle) Complete(_ context.Context, ids []string, cardID, content string, durationMS int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cardID == "" || content == "" {
		return errors.New("missing completed batch evidence")
	}
	f.completed = append(f.completed, ids...)
	f.durations = append(f.durations, durationMS)
	return nil
}

func (f *fakeLifecycle) Fail(_ context.Context, _ []string, code string, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failureCodes = append(f.failureCodes, code)
	return nil
}

func (f *fakeLifecycle) MarkCompanionDeliveryStarted(context.Context, []string, string) error {
	return nil
}

func (f *fakeLifecycle) CompleteCompanionDelivery(context.Context, []string, worker.CompanionDeliverySummary) error {
	return nil
}

func (f *fakeLifecycle) FailCompanionDelivery(_ context.Context, _ []string, _ string, code, _ string, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.companionFailureCodes = append(f.companionFailureCodes, code)
	return nil
}

func (f *fakeOutput) CreateBatchCard(_ context.Context, batch worker.Batch) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, batch)
	return "om_card_" + batch.ID, nil
}

func (f *fakeOutput) UpdateBatchCard(_ context.Context, _ string, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = append(f.updated, content)
	return f.updateErr
}

func (f *fakeOutput) UpdateBatchCardZones(_ context.Context, _ string, final, progress, summary string, closed bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zones = append(f.zones, struct {
		final, progress, summary string
		closed                   bool
	}{final: final, progress: progress, summary: summary, closed: closed})
	return nil
}

func TestManagerProjectsCompletedItemsIntoSeparateWorkCardZones(t *testing.T) {
	output := &fakeOutput{}
	manager := worker.NewManager(worker.Config{}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(_ context.Context, batch worker.Batch) (worker.ProcessResult, error) {
		if batch.OnItem == nil {
			return worker.ProcessResult{}, errors.New("missing presentation callback")
		}
		batch.OnItem(worker.PresentationItem{ID: "commentary-1", Type: "agentMessage", Phase: "commentary", Text: "progress"})
		batch.OnItem(worker.PresentationItem{ID: "final-1", Type: "agentMessage", Phase: "final_answer", Text: "answer"})
		return worker.ProcessResult{DurationMS: 1}, nil
	})
	defer manager.Close()
	key := worker.GroupKey("oc-zones", "app-zones")
	if err := manager.Accept(context.Background(), testMessage(key, "m-zones", "question")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		output.mu.Lock()
		zones := append([]struct {
			final, progress, summary string
			closed                   bool
		}(nil), output.zones...)
		output.mu.Unlock()
		if len(zones) >= 2 {
			last := zones[len(zones)-1]
			if last.final != "answer" || last.progress != "progress" || !last.closed {
				t.Fatalf("last zone update = %#v", last)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("zone updates = %#v", zones)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestWorkFailureFallsBackToTextWhenCardPatchFails(t *testing.T) {
	output := &fakeOutput{updateErr: errors.New("patch failed")}
	manager := worker.NewManager(worker.Config{}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(context.Context, worker.Batch) (worker.ProcessResult, error) {
		return worker.ProcessResult{}, errors.New("processor failed")
	})
	defer manager.Close()
	key := worker.GroupKey("oc_group", "app-a")
	if err := manager.Accept(context.Background(), testMessage(key, "m-1", "one")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		output.mu.Lock()
		texts := output.texts
		output.mu.Unlock()
		if texts == 1 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("failure was not sent as text fallback")
		case <-time.After(time.Millisecond):
		}
	}
}

func (f *fakeOutput) SendBatchText(_ context.Context, _ worker.ReplyTarget, text string) (string, error) {
	f.mu.Lock()
	f.texts++
	f.textBody = append(f.textBody, text)
	f.mu.Unlock()
	return "om_text", nil
}

func (f *fakeOutput) SendCompanionSegment(_ context.Context, _ worker.ReplyTarget, text string) worker.CompanionSendResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts++
	f.textBody = append(f.textBody, text)
	if len(f.companionOutcomes) == 0 {
		return worker.CompanionSendResult{MessageID: "om_text", Outcome: worker.CompanionSent}
	}
	result := f.companionOutcomes[0]
	f.companionOutcomes = f.companionOutcomes[1:]
	return result
}

func TestManagerBatchesMessagesArrivingDuringProcessingIntoOneNextCard(t *testing.T) {
	output := &fakeOutput{}
	started := make(chan worker.Batch, 1)
	release := make(chan struct{})
	manager := worker.NewManager(worker.Config{MaxWorkers: 20, QueueDepth: 64, ProcessTimeout: time.Hour}, func(batch worker.Batch) (worker.Output, error) {
		return output, nil
	}, func(ctx context.Context, batch worker.Batch) (worker.ProcessResult, error) {
		started <- batch
		select {
		case <-release:
			return worker.ProcessResult{}, nil
		case <-ctx.Done():
			return worker.ProcessResult{}, ctx.Err()
		}
	})
	defer manager.Close()

	key := worker.GroupKey("oc_group", "app-a")
	first := testMessage(key, "m-1", "first")
	if err := manager.Accept(context.Background(), first); err != nil {
		t.Fatalf("Accept(first): %v", err)
	}
	<-started
	for i, text := range []string{"two", "three", "four", "five"} {
		if err := manager.Accept(context.Background(), testMessage(key, "m-next-"+string(rune('2'+i)), text)); err != nil {
			t.Fatalf("Accept(%q): %v", text, err)
		}
	}
	close(release)

	deadline := time.After(2 * time.Second)
	for {
		output.mu.Lock()
		created := append([]worker.Batch(nil), output.created...)
		output.mu.Unlock()
		if len(created) == 2 {
			if got := created[1].Queries(); len(got) != 4 || got[0] != "two" || got[3] != "five" {
				t.Fatalf("second batch queries = %#v", got)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("created cards = %d, want 2", len(created))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestManagerMarksEveryMessageInABatchProcessingThenCompleted(t *testing.T) {
	output := &fakeOutput{}
	lifecycle := &fakeLifecycle{}
	manager := worker.NewManager(worker.Config{}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(context.Context, worker.Batch) (worker.ProcessResult, error) {
		return worker.ProcessResult{DurationMS: 25}, nil
	}, lifecycle)
	defer manager.Close()
	key := worker.GroupKey("oc_group", "app-a")
	if err := manager.Accept(context.Background(), testMessage(key, "m-1", "one")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		lifecycle.mu.Lock()
		done := len(lifecycle.completed)
		lifecycle.mu.Unlock()
		if done == 1 {
			lifecycle.mu.Lock()
			duration := lifecycle.durations[0]
			lifecycle.mu.Unlock()
			if duration <= 0 {
				t.Fatalf("duration = %d", duration)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("batch was not completed")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestManagerKeepsInProcessWorkerOutOfIdleRecycling(t *testing.T) {
	output := &fakeOutput{}
	started := make(chan struct{})
	release := make(chan struct{})
	manager := worker.NewManager(worker.Config{IdleTimeout: time.Millisecond, ProcessTimeout: time.Second}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(ctx context.Context, _ worker.Batch) (worker.ProcessResult, error) {
		close(started)
		select {
		case <-release:
			return worker.ProcessResult{}, nil
		case <-ctx.Done():
			return worker.ProcessResult{}, ctx.Err()
		}
	})
	defer manager.Close()
	key := worker.GroupKey("oc_group", "app-a")
	if err := manager.Accept(context.Background(), testMessage(key, "m-1", "one")); err != nil {
		t.Fatal(err)
	}
	<-started
	time.Sleep(10 * time.Millisecond)
	if state, ok := manager.State(key); !ok || state != worker.InProcess {
		t.Fatalf("state=%q exists=%v, want InProcess", state, ok)
	}
	close(release)
}

func TestManagerRecyclesOnlyIdleWorker(t *testing.T) {
	output := &fakeOutput{}
	manager := worker.NewManager(worker.Config{IdleTimeout: 10 * time.Millisecond}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(context.Context, worker.Batch) (worker.ProcessResult, error) { return worker.ProcessResult{}, nil })
	defer manager.Close()
	key := worker.GroupKey("oc_idle", "app-a")
	if err := manager.Accept(context.Background(), testMessage(key, "m-1", "one")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		if _, ok := manager.State(key); !ok {
			return
		}
		select {
		case <-deadline:
			t.Fatal("idle worker was not recycled")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestManagerStopsWorkerAfterProcessTimeout(t *testing.T) {
	output := &fakeOutput{}
	lifecycle := &fakeLifecycle{}
	manager := worker.NewManager(worker.Config{ProcessTimeout: 10 * time.Millisecond}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(ctx context.Context, _ worker.Batch) (worker.ProcessResult, error) {
		<-ctx.Done()
		return worker.ProcessResult{}, ctx.Err()
	}, lifecycle)
	defer manager.Close()
	key := worker.GroupKey("oc_timeout", "app-a")
	if err := manager.Accept(context.Background(), testMessage(key, "m-1", "one")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		if _, ok := manager.State(key); !ok {
			lifecycle.mu.Lock()
			completed := len(lifecycle.completed)
			failureCodes := append([]string(nil), lifecycle.failureCodes...)
			lifecycle.mu.Unlock()
			if completed != 0 {
				t.Fatalf("timeout batch completed %d messages", completed)
			}
			if len(failureCodes) != 1 || failureCodes[0] != "worker_timeout_stopped" {
				t.Fatalf("timeout failure codes = %#v", failureCodes)
			}
			output.mu.Lock()
			updates := append([]string(nil), output.updated...)
			output.mu.Unlock()
			if len(updates) != 1 || updates[0] != "本批处理超时，请重新发送。" {
				t.Fatalf("timeout card updates = %#v", updates)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("timed out worker was not stopped")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestManagerUsesPlainTextForCompanionBatch(t *testing.T) {
	output := &fakeOutput{}
	manager := worker.NewManager(worker.Config{}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(context.Context, worker.Batch) (worker.ProcessResult, error) {
		return worker.ProcessResult{DurationMS: 25}, nil
	})
	defer manager.Close()
	key := worker.P2PKey("ou_user", "app-a")
	message := testMessage(key, "m-1", "one")
	message.Runtime.WorkspaceMode = "companion"
	if err := manager.Accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		output.mu.Lock()
		created, texts := len(output.created), output.texts
		output.mu.Unlock()
		if texts == 1 {
			if created != 0 {
				t.Fatalf("companion created %d cards", created)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("companion text was not sent")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestCompanionSendsOnlyFinalAnswerSegmentsAfterSuccessfulTerminal(t *testing.T) {
	output := &fakeOutput{}
	manager := worker.NewManager(worker.Config{}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(_ context.Context, batch worker.Batch) (worker.ProcessResult, error) {
		if batch.OnItem == nil {
			return worker.ProcessResult{}, errors.New("missing presentation callback")
		}
		batch.OnItem(worker.PresentationItem{ID: "commentary", Type: "agentMessage", Phase: "commentary", Text: "do not send"})
		batch.OnItem(worker.PresentationItem{ID: "final", Type: "agentMessage", Phase: "final_answer", Text: "first[[SEND]]second"})
		return worker.ProcessResult{DurationMS: 1}, nil
	})
	defer manager.Close()
	key := worker.P2PKey("ou-companion", "app-companion")
	message := testMessage(key, "m-companion-final", "question")
	message.Runtime.WorkspaceMode = "companion"
	if err := manager.Accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		output.mu.Lock()
		texts := append([]string(nil), output.textBody...)
		output.mu.Unlock()
		if len(texts) == 2 {
			if texts[0] != "first" || texts[1] != "second" {
				t.Fatalf("texts = %#v", texts)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("texts = %#v", texts)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestCompanionUnknownSendStopsRemainingSegments(t *testing.T) {
	output := &fakeOutput{companionOutcomes: []worker.CompanionSendResult{{Outcome: worker.CompanionUnknown, Reason: "request_unknown"}}}
	manager := worker.NewManager(worker.Config{CompanionSegmentDelay: time.Millisecond}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(_ context.Context, batch worker.Batch) (worker.ProcessResult, error) {
		batch.OnItem(worker.PresentationItem{ID: "final", Type: "agentMessage", Phase: "final_answer", Text: "first[[SEND]]second"})
		return worker.ProcessResult{}, nil
	})
	defer manager.Close()
	message := testMessage(worker.P2PKey("ou-unknown", "app-companion"), "m-companion-unknown", "question")
	message.Runtime.WorkspaceMode = "companion"
	if err := manager.Accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		output.mu.Lock()
		count := output.texts
		output.mu.Unlock()
		if count >= 1 {
			time.Sleep(20 * time.Millisecond)
			output.mu.Lock()
			count = output.texts
			output.mu.Unlock()
			if count != 1 {
				t.Fatalf("unknown send attempted %d segments, want 1", count)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("unknown companion send was not attempted")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestCompanionWorkflowWriterFailureStopsLaterSegments(t *testing.T) {
	output := &fakeOutput{}
	lifecycle := &fakeLifecycle{}
	writer := worker.WorkflowWriterFunc(func(context.Context, worker.CompanionWorkflowEvent) error {
		return errors.New("workflow disk unavailable")
	})
	manager := worker.NewManager(worker.Config{CompanionSegmentDelay: time.Millisecond, WorkflowWriter: writer}, func(worker.Batch) (worker.Output, error) {
		return output, nil
	}, func(_ context.Context, batch worker.Batch) (worker.ProcessResult, error) {
		batch.OnItem(worker.PresentationItem{ID: "final", Type: "agentMessage", Phase: "final_answer", Text: "first[[SEND]]second"})
		return worker.ProcessResult{}, nil
	}, lifecycle)
	defer manager.Close()
	message := testMessage(worker.P2PKey("ou-workflow-failure", "app-companion"), "m-workflow-failure", "question")
	message.Runtime.WorkspaceMode = "companion"
	if err := manager.Accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		lifecycle.mu.Lock()
		codes := append([]string(nil), lifecycle.companionFailureCodes...)
		lifecycle.mu.Unlock()
		if len(codes) > 0 {
			output.mu.Lock()
			count := output.texts
			output.mu.Unlock()
			if count != 1 || codes[0] != "companion_delivery_trace_incomplete" {
				t.Fatalf("texts=%d failure_codes=%#v", count, codes)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("workflow writer failure did not finalize the batch")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestCancelWaitsForActiveChannelWorkerToRelease(t *testing.T) {
	output := &fakeOutput{}
	started := make(chan struct{})
	manager := worker.NewManager(worker.Config{}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(ctx context.Context, _ worker.Batch) (worker.ProcessResult, error) {
		close(started)
		<-ctx.Done()
		return worker.ProcessResult{}, ctx.Err()
	})
	defer manager.Close()
	key := worker.GroupKey("oc-cancel", "app-cancel")
	if err := manager.Accept(context.Background(), testMessage(key, "m-cancel", "question")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("processor did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Cancel(ctx, key); err != nil {
		t.Fatalf("Cancel() = %v", err)
	}
	deadline := time.After(time.Second)
	for {
		if state, exists := manager.State(key); exists && state == worker.Idle {
			return
		}
		select {
		case <-deadline:
			t.Fatal("worker did not release after cancel")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestManagerShutdownCancelsActiveTurnAndWaits(t *testing.T) {
	output := &fakeOutput{}
	started := make(chan struct{})
	cancelled := make(chan struct{})
	manager := worker.NewManager(worker.Config{}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(ctx context.Context, _ worker.Batch) (worker.ProcessResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return worker.ProcessResult{}, ctx.Err()
	})
	defer manager.Close()
	key := worker.GroupKey("oc-shutdown", "app-shutdown")
	if err := manager.Accept(context.Background(), testMessage(key, "m-shutdown", "question")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("processor did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() = %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("Shutdown returned before the active turn was cancelled")
	}
}

func TestCompanionWorkerStopsAfterProcessTimeout(t *testing.T) {
	output := &fakeOutput{}
	manager := worker.NewManager(worker.Config{ProcessTimeout: 10 * time.Millisecond}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(ctx context.Context, _ worker.Batch) (worker.ProcessResult, error) {
		<-ctx.Done()
		return worker.ProcessResult{}, ctx.Err()
	})
	defer manager.Close()
	key := worker.P2PKey("ou_timeout", "app-a")
	message := testMessage(key, "m-1", "one")
	message.Runtime.WorkspaceMode = "companion"
	if err := manager.Accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		if _, ok := manager.State(key); !ok {
			return
		}
		select {
		case <-deadline:
			t.Fatal("companion timeout worker was not stopped")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestManagerGoalIsExclusiveFIFOBarrier(t *testing.T) {
	output := &fakeOutput{}
	goalRelease := make(chan struct{})
	processed := make(chan string, 3)
	manager := worker.NewManager(worker.Config{}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(_ context.Context, batch worker.Batch) (worker.ProcessResult, error) {
		if batch.Goal {
			processed <- "goal"
			<-goalRelease
			return worker.ProcessResult{}, nil
		}
		processed <- batch.Messages[0].Query
		return worker.ProcessResult{}, nil
	})
	defer manager.Close()
	key := worker.GroupKey("oc-goal", "app-goal")
	if err := manager.Accept(context.Background(), testMessage(key, "m-a", "A")); err != nil {
		t.Fatal(err)
	}
	goal := testMessage(key, "m-goal", "objective")
	goal.Goal = true
	if err := manager.Accept(context.Background(), goal); err != nil {
		t.Fatal(err)
	}
	if err := manager.Accept(context.Background(), testMessage(key, "m-b", "B")); err != nil {
		t.Fatal(err)
	}
	if first := <-processed; first != "A" {
		t.Fatalf("first = %q", first)
	}
	if second := <-processed; second != "goal" {
		t.Fatalf("second = %q", second)
	}
	select {
	case got := <-processed:
		t.Fatalf("processed %q before goal terminal", got)
	case <-time.After(20 * time.Millisecond):
	}
	close(goalRelease)
	if last := <-processed; last != "B" {
		t.Fatalf("last = %q", last)
	}
}

func TestManagerGoalCompleteWithoutFinalShowsSafeNotice(t *testing.T) {
	output := &fakeOutput{}
	manager := worker.NewManager(worker.Config{}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(context.Context, worker.Batch) (worker.ProcessResult, error) {
		return worker.ProcessResult{}, nil
	})
	defer manager.Close()
	key := worker.GroupKey("oc-goal-empty", "app-goal")
	message := testMessage(key, "m-goal-empty", "objective")
	message.Goal = true
	if err := manager.Accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		output.mu.Lock()
		zones := append([]struct {
			final, progress, summary string
			closed                   bool
		}(nil), output.zones...)
		output.mu.Unlock()
		for _, zone := range zones {
			if zone.final == "目标已完成，但未收到可展示的最终摘要；请检查已完成的操作与日志。" && zone.progress == "目标已完成；本轮未收到可展示的阶段进展。" && zone.closed {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("goal terminal update = %#v", zones)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestManagerGoalFinalWithoutCommentaryClosesWithLifecycleProgress(t *testing.T) {
	output := &fakeOutput{}
	manager := worker.NewManager(worker.Config{}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(_ context.Context, batch worker.Batch) (worker.ProcessResult, error) {
		if batch.OnItem == nil {
			return worker.ProcessResult{}, errors.New("missing presentation callback")
		}
		batch.OnItem(worker.PresentationItem{ID: "final", Type: "agentMessage", Phase: "final_answer", Text: "verified final"})
		return worker.ProcessResult{}, nil
	})
	defer manager.Close()
	key := worker.GroupKey("oc-goal-final", "app-goal")
	message := testMessage(key, "m-goal-final", "objective")
	message.Goal = true
	if err := manager.Accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		output.mu.Lock()
		zones := append([]struct {
			final, progress, summary string
			closed                   bool
		}(nil), output.zones...)
		output.mu.Unlock()
		for _, zone := range zones {
			if zone.final == "verified final" && zone.progress == "目标已完成；本轮未收到可展示的阶段进展。" && zone.summary == "目标已完成" && zone.closed {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("goal terminal update = %#v", zones)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestManagerGoalPausedUsesGoalTerminalMessage(t *testing.T) {
	output := &fakeOutput{}
	manager := worker.NewManager(worker.Config{}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(context.Context, worker.Batch) (worker.ProcessResult, error) {
		return worker.ProcessResult{}, errors.New(`goal terminal status "paused"`)
	})
	defer manager.Close()
	key := worker.GroupKey("oc-goal-paused", "app-goal")
	message := testMessage(key, "m-goal-paused", "objective")
	message.Goal = true
	if err := manager.Accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		output.mu.Lock()
		zones := append([]struct {
			final, progress, summary string
			closed                   bool
		}(nil), output.zones...)
		output.mu.Unlock()
		for _, zone := range zones {
			if zone.final == "目标已暂停。" && zone.progress == "目标已暂停。" && zone.summary == "目标未完成" && zone.closed {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("goal paused update = %#v", zones)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestManagerControlRequestsGoalPauseBeforeReleasingBarrier(t *testing.T) {
	output := &fakeOutput{}
	stopped := &fakeGoalStopper{called: make(chan string, 1)}
	release := make(chan struct{})
	manager := worker.NewManager(worker.Config{}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(_ context.Context, batch worker.Batch) (worker.ProcessResult, error) {
		if batch.Goal {
			<-release
		}
		return worker.ProcessResult{}, nil
	})
	manager.SetGoalRunController(stopped)
	defer manager.Close()
	key := worker.GroupKey("oc-goal-control", "app-goal")
	goal := testMessage(key, "m-goal-control", "objective")
	goal.Goal = true
	if err := manager.Accept(context.Background(), goal); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		if state, ok := manager.State(key); ok && state == worker.InProcess {
			break
		}
		select {
		case <-deadline:
			t.Fatal("goal did not start")
		case <-time.After(time.Millisecond):
		}
	}
	if err := manager.SubmitControl(context.Background(), key, worker.Control{Run: func(context.Context) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	select {
	case batchID := <-stopped.called:
		if batchID == "" {
			t.Fatal("empty goal batch id")
		}
	case <-time.After(time.Second):
		t.Fatal("goal pause was not requested")
	}
	close(release)
}

func testMessage(key worker.Key, id, query string) worker.Message {
	return worker.Message{ID: id, TraceID: "trace-" + id, Key: key, Query: query, Runtime: worker.AppRuntime{ID: key.AppID, WorkspaceDir: "/tmp/workspace", Model: "gpt-test", Effort: "medium"}, Reply: worker.ReplyTarget{ID: key.Peer, Type: "chat_id"}}
}
