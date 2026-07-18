package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestGoalTerminalProgressUsesExplicitLifecycleStates(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{name: "paused", err: errors.New(`goal terminal status "paused"`), want: "目标已暂停。"},
		{name: "budget", err: errors.New(`goal terminal status "budget_limited"`), want: "目标达到预算限制，未宣告完成。"},
		{name: "timeout", err: context.DeadlineExceeded, want: "目标执行超时，未完成。"},
		{name: "cancelled", err: context.Canceled, want: "目标已中断。"},
		{name: "other", err: errors.New("failed"), want: "目标执行未完成。"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := goalTerminalProgress(test.err); got != test.want {
				t.Fatalf("goalTerminalProgress(%v) = %q, want %q", test.err, got, test.want)
			}
		})
	}
}

func TestNextBatchMakesScheduledPromptExclusive(t *testing.T) {
	w := &channelWorker{queue: []Message{
		{ID: "ordinary-1", Actor: ActorPrincipal{OpenID: "ou-1"}},
		{ID: "scheduled", Actor: ActorPrincipal{OpenID: "ou-1"}, ScheduledTaskRunID: "run-1"},
		{ID: "ordinary-2", Actor: ActorPrincipal{OpenID: "ou-1"}},
	}}
	batch, ok := w.nextBatch()
	if !ok || len(batch.Messages) != 1 || batch.Messages[0].ID != "ordinary-1" {
		t.Fatalf("first batch=%#v ok=%v", batch, ok)
	}
	w.processing = false
	batch, ok = w.nextBatch()
	if !ok || len(batch.Messages) != 1 || batch.Messages[0].ScheduledTaskRunID != "run-1" || batch.ScheduledTaskRunID != "run-1" {
		t.Fatalf("scheduled batch=%#v ok=%v", batch, ok)
	}
	if len(w.queue) != 1 || w.queue[0].ID != "ordinary-2" {
		t.Fatalf("remaining queue=%#v", w.queue)
	}
}

func TestScheduledPromptPassesFinalTextToDedicatedResultLifecycle(t *testing.T) {
	lifecycle := &scheduledResultLifecycle{done: make(chan struct{}, 1)}
	manager := NewManager(Config{}, nil, func(context.Context, Batch) (ProcessResult, error) {
		return ProcessResult{DurationMS: 12, FinalText: "scheduled final", ThreadID: "thread-scheduled", TurnID: "turn-scheduled"}, nil
	}, lifecycle)
	defer manager.Close()
	key := P2PKey("ou-scheduled", "app-scheduled")
	if err := manager.Accept(context.Background(), Message{ID: "m-scheduled", Key: key, Runtime: AppRuntime{ID: key.AppID}, Reply: ReplyTarget{ID: key.Peer, Type: "open_id"}, ScheduledTaskRunID: "run-1", ScheduledClaimToken: "claim-1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lifecycle.done:
		if lifecycle.finalText != "scheduled final" || lifecycle.threadID != "thread-scheduled" || lifecycle.turnID != "turn-scheduled" || lifecycle.oldCompleteCalls != 0 {
			t.Fatalf("final=%q thread=%q turn=%q old=%d", lifecycle.finalText, lifecycle.threadID, lifecycle.turnID, lifecycle.oldCompleteCalls)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduled result lifecycle was not invoked")
	}
}

func TestChannelControlCancelsDetachedScheduledPrompt(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	lifecycle := &scheduledResultLifecycle{done: make(chan struct{}, 1), cancelled: make(chan string, 1)}
	manager := NewManager(Config{}, func(Batch) (Output, error) { return &slotTestOutput{}, nil }, func(_ context.Context, batch Batch) (ProcessResult, error) {
		if batch.ScheduledTaskRunID == "" {
			close(started)
			<-release
		}
		return ProcessResult{}, nil
	}, lifecycle)
	defer manager.Close()
	key := P2PKey("ou-control", "app-control")
	if err := manager.Accept(context.Background(), Message{ID: "ordinary", Key: key, Runtime: AppRuntime{ID: key.AppID}, Reply: ReplyTarget{ID: key.Peer, Type: "open_id"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("ordinary batch did not start")
	}
	if err := manager.Accept(context.Background(), Message{ID: "scheduled", Key: key, Runtime: AppRuntime{ID: key.AppID}, Reply: ReplyTarget{ID: key.Peer, Type: "open_id"}, ScheduledTaskRunID: "run-detached", ScheduledClaimToken: "claim-detached"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.SubmitControl(context.Background(), key, Control{Run: func(context.Context) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-lifecycle.cancelled:
		if got != "run-detached:claim-detached:cancelled_by_channel_control" {
			t.Fatalf("cancelled run=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("detached scheduled prompt was not cancelled")
	}
	close(release)
}

func TestCancelMarksActiveScheduledPromptCancelledByChannelControl(t *testing.T) {
	started := make(chan struct{})
	lifecycle := &scheduledResultLifecycle{done: make(chan struct{}, 1), cancelled: make(chan string, 1)}
	manager := NewManager(Config{}, nil, func(ctx context.Context, _ Batch) (ProcessResult, error) {
		close(started)
		<-ctx.Done()
		return ProcessResult{}, ctx.Err()
	}, lifecycle)
	defer manager.Close()
	key := P2PKey("ou-active-control", "app-control")
	if err := manager.Accept(context.Background(), Message{ID: "scheduled", Key: key, Runtime: AppRuntime{ID: key.AppID}, Reply: ReplyTarget{ID: key.Peer, Type: "open_id"}, ScheduledTaskRunID: "run-active", ScheduledClaimToken: "claim-active"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("scheduled prompt did not start")
	}
	if err := manager.Cancel(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-lifecycle.cancelled:
		if got != "run-active:claim-active:cancelled_by_channel_control" {
			t.Fatalf("cancelled run=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("active scheduled prompt was not cancelled")
	}
}

func TestScheduledPromptTimeoutCancelsQueuedScheduledPrompts(t *testing.T) {
	started := make(chan struct{})
	secondProcessed := make(chan struct{}, 1)
	lifecycle := &scheduledResultLifecycle{done: make(chan struct{}, 2), cancelled: make(chan string, 1)}
	manager := NewManager(Config{ProcessTimeout: 50 * time.Millisecond}, nil, func(ctx context.Context, batch Batch) (ProcessResult, error) {
		if batch.ScheduledTaskRunID == "run-timeout" {
			close(started)
			<-ctx.Done()
			return ProcessResult{}, ctx.Err()
		}
		secondProcessed <- struct{}{}
		return ProcessResult{}, nil
	}, lifecycle)
	defer manager.Close()
	key := P2PKey("ou-scheduled-timeout", "app-timeout")
	if err := manager.Accept(context.Background(), Message{ID: "scheduled-timeout", Key: key, Runtime: AppRuntime{ID: key.AppID}, Reply: ReplyTarget{ID: key.Peer, Type: "open_id"}, ScheduledTaskRunID: "run-timeout", ScheduledClaimToken: "claim-timeout"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("scheduled prompt did not start")
	}
	if err := manager.Accept(context.Background(), Message{ID: "scheduled-queued", Key: key, Runtime: AppRuntime{ID: key.AppID}, Reply: ReplyTarget{ID: key.Peer, Type: "open_id"}, ScheduledTaskRunID: "run-queued", ScheduledClaimToken: "claim-queued"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-lifecycle.cancelled:
		if got != "run-queued:claim-queued:cancelled_by_channel_control" {
			t.Fatalf("cancelled run=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("queued scheduled prompt was not cancelled after timeout")
	}
	select {
	case <-secondProcessed:
		t.Fatal("queued scheduled prompt ran after preceding timeout")
	default:
	}
}

func TestManagerCloseCancelsQueuedScheduledPrompt(t *testing.T) {
	started := make(chan struct{})
	lifecycle := &scheduledResultLifecycle{done: make(chan struct{}, 1), cancelled: make(chan string, 1)}
	manager := NewManager(Config{}, func(Batch) (Output, error) { return &slotTestOutput{}, nil }, func(ctx context.Context, batch Batch) (ProcessResult, error) {
		if batch.ScheduledTaskRunID == "" {
			close(started)
			<-ctx.Done()
			return ProcessResult{}, ctx.Err()
		}
		return ProcessResult{}, nil
	}, lifecycle)
	key := P2PKey("ou-close", "app-close")
	if err := manager.Accept(context.Background(), Message{ID: "ordinary", Key: key, Runtime: AppRuntime{ID: key.AppID}, Reply: ReplyTarget{ID: key.Peer, Type: "open_id"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("ordinary batch did not start")
	}
	if err := manager.Accept(context.Background(), Message{ID: "scheduled", Key: key, Runtime: AppRuntime{ID: key.AppID}, Reply: ReplyTarget{ID: key.Peer, Type: "open_id"}, ScheduledTaskRunID: "run-close", ScheduledClaimToken: "claim-close"}); err != nil {
		t.Fatal(err)
	}
	manager.Close()
	select {
	case got := <-lifecycle.cancelled:
		if got != "run-close:claim-close:cancelled_by_channel_control" {
			t.Fatalf("cancelled run=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("queued scheduled prompt was not cancelled on manager close")
	}
}

type scheduledResultLifecycle struct {
	done             chan struct{}
	finalText        string
	threadID, turnID string
	oldCompleteCalls int
	cancelled        chan string
}

func (*scheduledResultLifecycle) MarkProcessing(context.Context, []string) error { return nil }
func (*scheduledResultLifecycle) Complete(context.Context, []string, string, string, int64) error {
	return nil
}
func (*scheduledResultLifecycle) Fail(context.Context, []string, string, int64) error { return nil }
func (*scheduledResultLifecycle) MarkScheduledRunning(context.Context, string, string) error {
	return nil
}
func (l *scheduledResultLifecycle) CompleteScheduledRun(context.Context, string, string, bool, string, int64) error {
	l.oldCompleteCalls++
	return nil
}
func (l *scheduledResultLifecycle) CompleteScheduledRunResult(_ context.Context, _ string, _ string, _ bool, _ string, _ int64, finalText, threadID, turnID string) error {
	l.finalText, l.threadID, l.turnID = finalText, threadID, turnID
	l.done <- struct{}{}
	return nil
}
func (l *scheduledResultLifecycle) DiscardScheduledRun(_ context.Context, runID, claimToken, code string) error {
	if l.cancelled != nil {
		l.cancelled <- runID + ":" + claimToken + ":" + code
	}
	return nil
}

type slotTestOutput struct {
	mu    sync.Mutex
	sends int
}

func (o *slotTestOutput) CreateBatchCard(context.Context, Batch) (string, error) { return "", nil }
func (o *slotTestOutput) UpdateBatchCard(context.Context, string, string) error  { return nil }
func (o *slotTestOutput) SendBatchText(context.Context, ReplyTarget, string) (string, error) {
	o.mu.Lock()
	o.sends++
	o.mu.Unlock()
	return "om-text", nil
}
func (o *slotTestOutput) SendCompanionSegment(context.Context, ReplyTarget, string) CompanionSendResult {
	o.mu.Lock()
	o.sends++
	o.mu.Unlock()
	return CompanionSendResult{MessageID: "om-text", Outcome: CompanionSent}
}

func TestCompanionCancelBeforeDeliveryPublicationSendsNoSegment(t *testing.T) {
	output := &slotTestOutput{}
	ready := make(chan struct{})
	release := make(chan struct{})
	manager := NewManager(Config{CompanionSegmentDelay: time.Millisecond}, func(Batch) (Output, error) { return output, nil }, func(_ context.Context, batch Batch) (ProcessResult, error) {
		batch.OnItem(PresentationItem{ID: "final", Type: "agentMessage", Phase: "final_answer", Text: "first[[SEND]]second"})
		close(ready)
		<-release
		return ProcessResult{}, nil
	})
	defer manager.Close()
	key := P2PKey("ou-pre-delivery-cancel", "app-companion")
	message := Message{ID: "m-pre-delivery-cancel", TraceID: "trace-pre-delivery-cancel", Key: key, Runtime: AppRuntime{ID: key.AppID, WorkspaceMode: "companion"}, Reply: ReplyTarget{ID: key.Peer, Type: "open_id"}}
	if err := manager.Accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("processor did not reach terminal boundary")
	}

	manager.mu.Lock()
	w := manager.workers[key.String()]
	manager.mu.Unlock()
	if w == nil {
		t.Fatal("channel worker missing")
	}
	w.mu.Lock()
	slot := w.delivery
	w.mu.Unlock()
	if slot == nil {
		t.Fatal("companion delivery slot missing")
	}
	cancelled := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		cancelled <- manager.Cancel(ctx, key)
	}()
	deadline := time.After(time.Second)
	for {
		slot.mu.Lock()
		latched := slot.cancelled
		slot.mu.Unlock()
		if latched {
			break
		}
		select {
		case <-deadline:
			t.Fatal("cancel did not latch companion delivery slot")
		case <-time.After(time.Millisecond):
		}
	}
	close(release)
	if err := <-cancelled; err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	output.mu.Lock()
	sends := output.sends
	output.mu.Unlock()
	if sends != 0 {
		t.Fatalf("visible companion sends = %d, want 0", sends)
	}
}

func TestNextBatchKeepsRequiredAttachmentAsAnExclusiveFIFOBarrier(t *testing.T) {
	manager := NewManager(Config{}, nil, nil)
	key := GroupKey("oc-attachment-boundary", "app-a")
	attachment := Message{ID: "m-image", Key: key, HasRequiredAttachment: true}
	worker := &channelWorker{
		manager: manager,
		key:     key,
		queue: []Message{
			{ID: "m-text-before-1", Key: key},
			{ID: "m-text-before-2", Key: key},
			attachment,
			{ID: "m-text-after", Key: key},
		},
	}

	first, ok := worker.nextBatch()
	if !ok || len(first.Messages) != 2 || first.Messages[0].ID != "m-text-before-1" || first.Messages[1].ID != "m-text-before-2" {
		t.Fatalf("first batch = %#v, ok=%v", first.Messages, ok)
	}
	worker.processing = false
	second, ok := worker.nextBatch()
	if !ok || len(second.Messages) != 1 || second.Messages[0].ID != attachment.ID {
		t.Fatalf("attachment batch = %#v, ok=%v", second.Messages, ok)
	}
	worker.processing = false
	third, ok := worker.nextBatch()
	if !ok || len(third.Messages) != 1 || third.Messages[0].ID != "m-text-after" {
		t.Fatalf("third batch = %#v, ok=%v", third.Messages, ok)
	}
}

func TestNextBatchDoesNotMergeDifferentActors(t *testing.T) {
	manager := NewManager(Config{}, nil, nil)
	key := GroupKey("oc-actor-boundary", "app-a")
	channel := &channelWorker{manager: manager, key: key, queue: []Message{
		{ID: "m-a-1", Key: key, Actor: ActorPrincipal{OpenID: "ou-a"}},
		{ID: "m-a-2", Key: key, Actor: ActorPrincipal{OpenID: "ou-a"}},
		{ID: "m-b-1", Key: key, Actor: ActorPrincipal{OpenID: "ou-b"}},
	}}
	batch, ok := channel.nextBatch()
	if !ok || len(batch.Messages) != 2 || batch.Messages[0].Actor.OpenID != "ou-a" || batch.Messages[1].Actor.OpenID != "ou-a" {
		t.Fatalf("first batch = %#v ok=%t", batch.Messages, ok)
	}
	channel.processing = false
	batch, ok = channel.nextBatch()
	if !ok || len(batch.Messages) != 1 || batch.Messages[0].Actor.OpenID != "ou-b" {
		t.Fatalf("second batch = %#v ok=%t", batch.Messages, ok)
	}
}
