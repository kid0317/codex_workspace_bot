package observability

import (
	"context"
	"log/slog"
	"time"
)

const (
	EventReceiverAccepted   = "receiver_message_accepted"
	EventDispatchQueued     = "dispatch_queued"
	EventDispatchRejected   = "dispatch_rejected"
	EventWorkerStarted      = "worker_started"
	EventWorkerExited       = "worker_exited"
	EventSessionCreated     = "session_created"
	EventSessionReused      = "session_reused"
	EventSessionArchived    = "session_archived"
	EventContextWritten     = "context_written"
	EventTurnStarted        = "turn_started"
	EventDeltaReceived      = "delta_received"
	EventApprovalRequested  = "approval_requested"
	EventApprovalResolved   = "approval_resolved"
	EventTurnInterrupted    = "turn_interrupted"
	EventTurnCompleted      = "turn_completed"
	EventTurnFailed         = "turn_failed"
	EventTaskScheduled      = "task_scheduled"
	EventTaskStarted        = "task_started"
	EventTaskCompleted      = "task_completed"
	EventTaskFailed         = "task_failed"
	EventAttachmentPending  = "attachment_pending"
	EventAttachmentConsumed = "attachment_consumed"
	EventAttachmentExpired  = "attachment_expired"
	EventAttachmentCleared  = "attachment_cleared"
)

type Event struct {
	AppID          string    `json:"app_id"`
	ChannelKey     string    `json:"channel_key"`
	SessionID      string    `json:"session_id"`
	EngineThreadID string    `json:"engine_thread_id"`
	MessageID      string    `json:"message_id"`
	TaskID         string    `json:"task_id"`
	TurnID         string    `json:"turn_id"`
	EventType      string    `json:"event_type"`
	DurationMS     int64     `json:"duration_ms"`
	InputTokens    int       `json:"input_tokens"`
	OutputTokens   int       `json:"output_tokens"`
	ErrorKind      string    `json:"error_kind,omitempty"`
	At             time.Time `json:"at"`
}

type Emitter interface {
	Emit(ctx context.Context, ev Event)
}

type NopEmitter struct{}

func (NopEmitter) Emit(ctx context.Context, ev Event) {}

type SlogEmitter struct {
	Logger *slog.Logger
}

func (e SlogEmitter) Emit(ctx context.Context, ev Event) {
	logger := e.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.InfoContext(ctx, ev.EventType,
		"app_id", ev.AppID,
		"channel_key", ev.ChannelKey,
		"session_id", ev.SessionID,
		"engine_thread_id", ev.EngineThreadID,
		"message_id", ev.MessageID,
		"task_id", ev.TaskID,
		"turn_id", ev.TurnID,
		"duration_ms", ev.DurationMS,
		"input_tokens", ev.InputTokens,
		"output_tokens", ev.OutputTokens,
		"error_kind", ev.ErrorKind,
	)
}
