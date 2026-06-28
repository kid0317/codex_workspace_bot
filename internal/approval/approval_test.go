package approval_test

import (
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/approval"
)

func TestStateMachineAllowsExpectedTransitionsOnly(t *testing.T) {
	req := approval.Request{ID: "a1", Status: approval.StatusRequested, ExpiresAt: time.Now().Add(time.Minute)}
	if err := req.Transition(approval.StatusPendingUser); err != nil {
		t.Fatalf("Transition pending error = %v", err)
	}
	if err := req.Transition(approval.StatusUserAllowed); err != nil {
		t.Fatalf("Transition allow error = %v", err)
	}
	if err := req.Transition(approval.StatusExpired); err == nil {
		t.Fatal("terminal approval should reject later transition")
	}
}
