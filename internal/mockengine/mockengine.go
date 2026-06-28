package mockengine

import (
	"context"

	"github.com/google/uuid"
	"github.com/kid0317/codex-workspace-bot/internal/engine"
)

type Engine struct{}

func New() *Engine { return &Engine{} }

func (e *Engine) SendTurn(ctx context.Context, req engine.TurnRequest) (engine.EventStream, error) {
	threadID := req.ThreadID
	if req.ThreadPolicy == engine.ThreadForceNew || threadID == "" {
		threadID = "thread-" + uuid.NewString()
	}
	start := engine.TurnEvent{Type: engine.EventTurnStarted, ThreadID: threadID}
	switch req.Scenario {
	case "engine_error":
		return engine.NewSliceStream([]engine.TurnEvent{start, {Type: engine.EventFailed, ThreadID: threadID, Error: "mock engine error"}}), nil
	case "empty_output":
		return engine.NewSliceStream([]engine.TurnEvent{start, {Type: engine.EventCompleted, ThreadID: threadID}}), nil
	case "approval_timeout":
		return engine.NewSliceStream([]engine.TurnEvent{start, {Type: engine.EventApprovalRequested, ThreadID: threadID, ApprovalID: "approval-timeout", ApprovalJSON: `{"action":"timeout"}`}, {Type: engine.EventFailed, ThreadID: threadID, Error: "approval timeout"}}), nil
	case "approval_requested":
		return engine.NewSliceStream([]engine.TurnEvent{start, {Type: engine.EventApprovalRequested, ThreadID: threadID, ApprovalID: "approval-requested", ApprovalJSON: `{"action":"mock"}`}, {Type: engine.EventFailed, ThreadID: threadID, Error: "approval required"}}), nil
	default:
		return engine.NewSliceStream([]engine.TurnEvent{start, {Type: engine.EventDelta, ThreadID: threadID, Text: "hello"}, {Type: engine.EventCompleted, ThreadID: threadID, InputTokens: 1, OutputTokens: 1}}), nil
	}
}
