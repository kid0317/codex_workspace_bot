package guardrail_test

import (
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/guardrail"
)

func TestLimitsAllowAtLimitAndRejectLimitPlusOne(t *testing.T) {
	g := guardrail.New(guardrail.Config{MaxMessageBytes: 4, MaxOutputBytes: 5, MaxEventsPerTurn: 2, MaxTurnDuration: time.Second, AllowedChats: []string{"chat-ok"}})
	if err := g.CheckInput("1234", "chat-ok"); err != nil {
		t.Fatalf("at input limit error = %v", err)
	}
	if err := g.CheckInput("12345", "chat-ok"); err == nil {
		t.Fatal("input limit plus one should fail")
	}
	if err := g.CheckInput("1", "chat-bad"); err == nil {
		t.Fatal("disallowed chat should fail")
	}
	if err := g.CheckOutput("123456"); err == nil {
		t.Fatal("output limit plus one should fail")
	}
	if err := g.CheckEventCount(3); err == nil {
		t.Fatal("event count over limit should fail")
	}
}
