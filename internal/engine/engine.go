package engine

import (
	"context"
	"errors"
	"fmt"
)

type ThreadPolicy string

const (
	ThreadResumeExisting ThreadPolicy = "resume_existing"
	ThreadForceNew       ThreadPolicy = "force_new"
	ThreadNoPersist      ThreadPolicy = "no_persist"
)

type EventType string

const (
	EventTurnStarted       EventType = "turn_started"
	EventDelta             EventType = "delta"
	EventApprovalRequested EventType = "approval_requested"
	EventCompleted         EventType = "completed"
	EventFailed            EventType = "failed"
	EventInterrupted       EventType = "interrupted"
)

type TurnRequest struct {
	Prompt       string
	Scenario     string
	ThreadID     string
	ThreadPolicy ThreadPolicy
}

type TurnEvent struct {
	Type         EventType
	Text         string
	ThreadID     string
	ApprovalID   string
	ApprovalJSON string
	InputTokens  int
	OutputTokens int
	Error        string
}

type EventStream interface {
	Next() bool
	Event() TurnEvent
	Err() error
}

type Engine interface {
	SendTurn(ctx context.Context, req TurnRequest) (EventStream, error)
}

func ValidateEvents(events []TurnEvent) error {
	if len(events) == 0 {
		return errors.New("事件流为空")
	}
	terminal := 0
	seenTerminal := false
	for i, ev := range events {
		if i == 0 && ev.Type != EventTurnStarted {
			return fmt.Errorf("首个事件必须是 turn_started，实际为 %s", ev.Type)
		}
		if seenTerminal {
			return fmt.Errorf("终态之后出现事件: %s", ev.Type)
		}
		switch ev.Type {
		case EventCompleted, EventFailed, EventInterrupted:
			terminal++
			seenTerminal = true
		case EventTurnStarted, EventDelta, EventApprovalRequested:
		default:
			return fmt.Errorf("未知事件类型: %s", ev.Type)
		}
	}
	if terminal != 1 {
		return fmt.Errorf("终态事件数量 = %d", terminal)
	}
	return nil
}

type SliceStream struct {
	events  []TurnEvent
	idx     int
	current TurnEvent
	err     error
}

func NewSliceStream(events []TurnEvent) *SliceStream {
	return &SliceStream{events: events}
}

func (s *SliceStream) Next() bool {
	if s.idx >= len(s.events) {
		return false
	}
	s.current = s.events[s.idx]
	s.idx++
	return true
}

func (s *SliceStream) Event() TurnEvent { return s.current }
func (s *SliceStream) Err() error       { return s.err }
