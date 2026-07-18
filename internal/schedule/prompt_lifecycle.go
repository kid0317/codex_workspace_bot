package schedule

import (
	"context"
	"fmt"
	"time"
)

type PromptRunStore interface {
	MarkRunRunning(context.Context, string, string, time.Duration) error
	CompleteRun(context.Context, RunCompletion) error
}

type PromptResultDelivery interface {
	Deliver(context.Context, string, ResultPresentation) error
}

// PromptRunLifecycle is the Worker-facing S06 lifecycle adapter. It completes
// the immutable execution run before any external result delivery; a delivery
// failure is observable through its outbox but never rewrites execution truth.
type PromptRunLifecycle struct {
	Runs         PromptRunStore
	Delivery     PromptResultDelivery
	RunningLease time.Duration
}

func (l PromptRunLifecycle) MarkScheduledRunning(ctx context.Context, runID, claimToken string) error {
	if l.Runs == nil {
		return fmt.Errorf("scheduled prompt run store is unavailable")
	}
	lease := l.RunningLease
	if lease <= 0 {
		lease = 330 * time.Second
	}
	return l.Runs.MarkRunRunning(ctx, runID, claimToken, lease)
}

func (l PromptRunLifecycle) CompleteScheduledRun(ctx context.Context, runID, claimToken string, succeeded bool, code string, durationMS int64) error {
	return l.CompleteScheduledRunResult(ctx, runID, claimToken, succeeded, code, durationMS, "", "", "")
}

// DiscardScheduledRun records a queued prompt removed by a same-channel
// control barrier. It must not create an outbox delivery because the prompt
// never became visible as completed work.
func (l PromptRunLifecycle) DiscardScheduledRun(ctx context.Context, runID, claimToken, code string) error {
	if l.Runs == nil {
		return fmt.Errorf("scheduled prompt run store is unavailable")
	}
	return l.Runs.CompleteRun(ctx, RunCompletion{RunID: runID, ClaimToken: claimToken, State: RunCancelled, ErrorCode: code})
}

func (l PromptRunLifecycle) CompleteScheduledRunResult(ctx context.Context, runID, claimToken string, succeeded bool, code string, durationMS int64, finalText, threadID, turnID string) error {
	if l.Runs == nil {
		return fmt.Errorf("scheduled prompt run store is unavailable")
	}
	state := RunFailed
	if succeeded {
		state, code = RunSucceeded, ""
	}
	if err := l.Runs.CompleteRun(ctx, RunCompletion{RunID: runID, ClaimToken: claimToken, State: state, ErrorCode: code, DurationMS: durationMS, ThreadID: threadID, TurnID: turnID}); err != nil {
		return err
	}
	if l.Delivery == nil {
		return fmt.Errorf("scheduled prompt result delivery is unavailable")
	}
	// The final answer is retained only in this call frame and in the static
	// card constructed by ResultDeliveryDispatcher.
	return l.Delivery.Deliver(ctx, runID, ResultPresentation{Succeeded: succeeded, ErrorCode: code, FinalText: finalText})
}
