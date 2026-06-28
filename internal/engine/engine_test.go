package engine_test

import (
	"context"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/engine"
	"github.com/kid0317/codex-workspace-bot/internal/mockengine"
)

func TestMockEngineScenariosArePerRequestAndTerminal(t *testing.T) {
	e := mockengine.New()
	stream, err := e.SendTurn(context.Background(), engine.TurnRequest{Prompt: "hi", Scenario: "normal_delta", ThreadPolicy: engine.ThreadResumeExisting})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, stream)
	if events[0].Type != engine.EventTurnStarted || events[len(events)-1].Type != engine.EventCompleted {
		t.Fatalf("events = %#v", events)
	}
	errStream, err := e.SendTurn(context.Background(), engine.TurnRequest{Prompt: "hi", Scenario: "engine_error", ThreadPolicy: engine.ThreadForceNew})
	if err != nil {
		t.Fatal(err)
	}
	errEvents := collect(t, errStream)
	if errEvents[len(errEvents)-1].Type != engine.EventFailed {
		t.Fatalf("error scenario events = %#v", errEvents)
	}
}

func collect(t *testing.T, stream engine.EventStream) []engine.TurnEvent {
	t.Helper()
	var out []engine.TurnEvent
	for stream.Next() {
		out = append(out, stream.Event())
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if err := engine.ValidateEvents(out); err != nil {
		t.Fatal(err)
	}
	return out
}
