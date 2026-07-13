package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

func TestTerminalArbiterKeepsFirstReason(t *testing.T) {
	arbiter := worker.NewTerminalArbiter()
	if !arbiter.Claim("completed") {
		t.Fatal("first terminal claim was rejected")
	}
	if arbiter.Claim("cancelled") {
		t.Fatal("second terminal claim was accepted")
	}
	if got := arbiter.Reason(); got != "completed" {
		t.Fatalf("Reason() = %q, want completed", got)
	}
}

func TestDeliverySlotCancelBeforePublishPreventsVisibleDelivery(t *testing.T) {
	slot := worker.NewDeliverySlot()
	if !slot.Begin() {
		t.Fatal("Begin() = false")
	}
	finished := make(chan struct{})
	go func() {
		if err := slot.CancelAndWait(context.Background()); err != nil {
			t.Errorf("CancelAndWait() error = %v", err)
		}
		close(finished)
	}()

	select {
	case <-finished:
		t.Fatal("CancelAndWait returned before Finish")
	case <-time.After(10 * time.Millisecond):
	}
	if _, ok := slot.Publish(context.Background()); ok {
		t.Fatal("Publish() succeeded after cancellation latch")
	}
	slot.Finish()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("CancelAndWait did not return after Finish")
	}
}

func TestDeliverySlotCancelAfterPublishCancelsDeliveryContext(t *testing.T) {
	slot := worker.NewDeliverySlot()
	if !slot.Begin() {
		t.Fatal("Begin() = false")
	}
	deliveryCtx, ok := slot.Publish(context.Background())
	if !ok {
		t.Fatal("Publish() = false")
	}
	finished := make(chan struct{})
	go func() {
		if err := slot.CancelAndWait(context.Background()); err != nil {
			t.Errorf("CancelAndWait() error = %v", err)
		}
		close(finished)
	}()
	select {
	case <-deliveryCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("published context was not cancelled")
	}
	slot.Finish()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("CancelAndWait did not return after Finish")
	}
}
