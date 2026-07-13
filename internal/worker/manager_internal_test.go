package worker

import (
	"context"
	"sync"
	"testing"
	"time"
)

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
