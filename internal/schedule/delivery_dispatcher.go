package schedule

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

// ErrDeliveryCompletionLost means the external outcome was observed but the
// conditional terminal write no longer owned the in-flight intent.
var ErrDeliveryCompletionLost = errors.New("scheduled delivery completion was not persisted")

// DeliveryRoute is the memory-only destination for one terminal run. It is
// loaded from the task owner record only after the run has reached terminal
// state and must never be logged or persisted by a delivery caller.
type DeliveryRoute struct {
	RunID, AppID, ReplyID, ReplyType string
	Kind                             TaskKind
	Silent                           bool
}

// ResultPresentation is held only for the duration of terminal delivery.
// ScriptConsole is intentionally rendered only into its primary result card;
// it is never used by the text fallback or persisted in the delivery table.
type ResultPresentation struct {
	Succeeded                bool
	ErrorCode, FinalText     string
	ScriptConsole            string
	ExitCode                 int
	ScriptOutputWasTruncated bool
}

type ResultDeliveryStore interface {
	LoadDeliveryRoute(context.Context, string) (DeliveryRoute, error)
	CreateResultDelivery(context.Context, string, bool) (DeliveryIntent, error)
	ClaimDelivery(context.Context, string) (bool, error)
	CompleteDelivery(context.Context, string, DeliveryOutcome, string) (bool, error)
	CreateFallbackAfterRejected(context.Context, string) (DeliveryIntent, error)
	MessageIDHMAC(string) (string, error)
}

type ResultDeliverySender interface {
	SendStaticCard(context.Context, string, string, string) (string, error)
	SendCommandText(context.Context, string, string, string) (string, error)
}

// ResultDeliveryDispatcher owns the narrow outbox-to-Feishu boundary. It
// never retries an in-flight request and only falls back after the sender has
// classified the primary card as explicitly rejected.
type ResultDeliveryDispatcher struct {
	Store        ResultDeliveryStore
	Sender       ResultDeliverySender
	SenderForApp func(string) ResultDeliverySender
}

func (d ResultDeliveryDispatcher) Deliver(ctx context.Context, runID string, presentation ResultPresentation) error {
	if d.Store == nil || strings.TrimSpace(runID) == "" {
		return fmt.Errorf("scheduled result delivery is not configured")
	}
	// Create the immutable terminal intent before resolving a mutable route.
	// In particular, a silent run must be persisted as suppressed even if its
	// owner route was revoked after execution completed.
	intent, err := d.Store.CreateResultDelivery(ctx, runID, false)
	if err != nil {
		return err
	}
	if intent.Outcome == DeliverySuppressed || intent.ID == "" {
		return nil
	}
	if d.Sender == nil && d.SenderForApp == nil {
		return fmt.Errorf("scheduled result sender is unavailable")
	}
	route, err := d.Store.LoadDeliveryRoute(ctx, runID)
	if err != nil {
		return err
	}
	claimed, err := d.Store.ClaimDelivery(ctx, intent.ID)
	if err != nil || !claimed {
		return err
	}
	sender := d.senderFor(route.AppID)
	if sender == nil {
		return fmt.Errorf("scheduled result sender is unavailable")
	}
	messageID, sendErr := sender.SendStaticCard(ctx, route.ReplyID, route.ReplyType, renderResultCard(route.Kind, presentation))
	outcome := classifyDeliveryError(sendErr)
	messageHMAC := ""
	if outcome == DeliverySent {
		messageHMAC, err = d.Store.MessageIDHMAC(messageID)
		if err != nil {
			// A send may already be visible but lacks a safe audit HMAC: do not
			// turn it into a retryable state.
			outcome = DeliveryUnknown
		}
	}
	completed, err := d.Store.CompleteDelivery(ctx, intent.ID, outcome, messageHMAC)
	if err != nil {
		return err
	}
	if !completed {
		return ErrDeliveryCompletionLost
	}
	if outcome != DeliveryRejected {
		return nil
	}
	return d.sendFallback(ctx, route, runID)
}

func (d ResultDeliveryDispatcher) sendFallback(ctx context.Context, route DeliveryRoute, runID string) error {
	intent, err := d.Store.CreateFallbackAfterRejected(ctx, runID)
	if err != nil || intent.ID == "" {
		return err
	}
	claimed, err := d.Store.ClaimDelivery(ctx, intent.ID)
	if err != nil || !claimed {
		return err
	}
	sender := d.senderFor(route.AppID)
	if sender == nil {
		return fmt.Errorf("scheduled result sender is unavailable")
	}
	messageID, sendErr := sender.SendCommandText(ctx, route.ReplyID, route.ReplyType, "计划任务已结束，但结果卡未被平台接受。")
	outcome := classifyDeliveryError(sendErr)
	messageHMAC := ""
	if outcome == DeliverySent {
		messageHMAC, err = d.Store.MessageIDHMAC(messageID)
		if err != nil {
			outcome = DeliveryUnknown
		}
	}
	completed, err := d.Store.CompleteDelivery(ctx, intent.ID, outcome, messageHMAC)
	if err != nil {
		return err
	}
	if !completed {
		return ErrDeliveryCompletionLost
	}
	return nil
}

func (d ResultDeliveryDispatcher) senderFor(appID string) ResultDeliverySender {
	if d.SenderForApp != nil {
		return d.SenderForApp(appID)
	}
	return d.Sender
}

func classifyDeliveryError(err error) DeliveryOutcome {
	if err == nil {
		return DeliverySent
	}
	if errors.Is(err, worker.ErrCommandDeliveryRejected) {
		return DeliveryRejected
	}
	return DeliveryUnknown
}

func renderResultCard(kind TaskKind, presentation ResultPresentation) string {
	if kind == TaskScript {
		status := "成功"
		if !presentation.Succeeded {
			status = "失败"
		}
		truncated := "否"
		if presentation.ScriptOutputWasTruncated {
			truncated = "是"
		}
		console := strings.TrimSpace(presentation.ScriptConsole)
		if console == "" {
			console = "（无输出）"
		}
		return "计划脚本执行" + status + "\nexit_code: " + fmt.Sprint(presentation.ExitCode) + "\n输出已截断: " + truncated + "\n\n" + console
	}
	if presentation.Succeeded {
		if text := strings.TrimSpace(presentation.FinalText); text != "" {
			return text
		}
		return "计划任务已完成。"
	}
	return "计划任务执行失败。"
}
