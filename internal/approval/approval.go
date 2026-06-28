package approval

import (
	"fmt"
	"time"
)

type Status string

const (
	StatusRequested   Status = "requested"
	StatusAutoAllowed Status = "auto_allowed"
	StatusAutoDenied  Status = "auto_denied"
	StatusPendingUser Status = "pending_user"
	StatusUserAllowed Status = "user_allowed"
	StatusUserDenied  Status = "user_denied"
	StatusExpired     Status = "expired"
	StatusInterrupted Status = "interrupted"
)

type Request struct {
	ID             string
	AppID          string
	ChannelKey     string
	SessionID      string
	TurnID         string
	EngineThreadID string
	Status         Status
	CreatedAt      time.Time
	ResolvedAt     *time.Time
	ExpiresAt      time.Time
}

func (r *Request) Transition(next Status) error {
	if !allowed(r.Status, next) {
		return fmt.Errorf("审批状态不能从 %s 切换到 %s", r.Status, next)
	}
	r.Status = next
	if terminal(next) {
		now := time.Now()
		r.ResolvedAt = &now
	}
	return nil
}

func allowed(from, to Status) bool {
	switch from {
	case StatusRequested:
		return to == StatusAutoAllowed || to == StatusAutoDenied || to == StatusPendingUser || to == StatusInterrupted
	case StatusPendingUser:
		return to == StatusUserAllowed || to == StatusUserDenied || to == StatusExpired || to == StatusInterrupted
	default:
		return false
	}
}

func terminal(s Status) bool {
	return s != StatusRequested && s != StatusPendingUser
}
