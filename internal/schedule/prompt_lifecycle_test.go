package schedule

import (
	"context"
	"testing"
	"time"
)

func TestPromptRunLifecycleCompletesRunBeforeDeliveringEphemeralFinalText(t *testing.T) {
	runs := &fakePromptRunStore{}
	delivery := &fakePromptDelivery{}
	lifecycle := PromptRunLifecycle{Runs: runs, Delivery: delivery, RunningLease: 5 * time.Minute}
	if err := lifecycle.MarkScheduledRunning(context.Background(), "run-1", "claim-1"); err != nil {
		t.Fatalf("MarkScheduledRunning() error=%v", err)
	}
	if err := lifecycle.CompleteScheduledRunResult(context.Background(), "run-1", "claim-1", true, "", 123, "final only in memory", "thread-1", "turn-1"); err != nil {
		t.Fatalf("CompleteScheduledRunResult() error=%v", err)
	}
	if runs.completion.State != RunSucceeded || runs.completion.DurationMS != 123 || runs.completion.ThreadID != "thread-1" || runs.completion.TurnID != "turn-1" || delivery.runID != "run-1" || !delivery.presentation.Succeeded || delivery.presentation.FinalText != "final only in memory" {
		t.Fatalf("runs=%#v delivery=%#v", runs, delivery)
	}
}

func TestPromptRunLifecycleDiscardCancelsWithoutResultDelivery(t *testing.T) {
	runs := &fakePromptRunStore{}
	delivery := &fakePromptDelivery{}
	lifecycle := PromptRunLifecycle{Runs: runs, Delivery: delivery}
	if err := lifecycle.DiscardScheduledRun(context.Background(), "run-1", "claim-1", "cancelled_by_channel_control"); err != nil {
		t.Fatalf("DiscardScheduledRun() error=%v", err)
	}
	if runs.completion.State != RunCancelled || runs.completion.ErrorCode != "cancelled_by_channel_control" {
		t.Fatalf("completion=%#v", runs.completion)
	}
	if delivery.runID != "" {
		t.Fatalf("delivery=%#v", delivery)
	}
}

type fakePromptRunStore struct {
	markedRun, markedClaim string
	lease                  time.Duration
	completion             RunCompletion
}

func (s *fakePromptRunStore) MarkRunRunning(_ context.Context, runID, claim string, lease time.Duration) error {
	s.markedRun, s.markedClaim, s.lease = runID, claim, lease
	return nil
}
func (s *fakePromptRunStore) CompleteRun(_ context.Context, completion RunCompletion) error {
	s.completion = completion
	return nil
}

type fakePromptDelivery struct {
	runID        string
	presentation ResultPresentation
	done         chan struct{}
}

func (d *fakePromptDelivery) Deliver(_ context.Context, runID string, presentation ResultPresentation) error {
	d.runID, d.presentation = runID, presentation
	if d.done != nil {
		d.done <- struct{}{}
	}
	return nil
}
