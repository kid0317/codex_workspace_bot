package main

import (
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

func TestIsScheduleToolAcceptsAppServerBareNames(t *testing.T) {
	for _, tool := range []string{"schedule.list_own", "list_own", "schedule.create", "create", "schedule.update", "update"} {
		if !isScheduleTool(tool) {
			t.Fatalf("isScheduleTool(%q) = false", tool)
		}
	}
	if isScheduleTool("feishu.message_send_current_channel") {
		t.Fatal("Feishu tool must not enter schedule route")
	}
}

func TestBatchActorOpenIDRequiresOneNonEmptyActor(t *testing.T) {
	key := worker.GroupKey("oc-group", "app-1")
	if got := batchActorOpenID(worker.Batch{Messages: []worker.Message{{Key: key, Actor: worker.ActorPrincipal{OpenID: "ou-sender"}}, {Key: key, Actor: worker.ActorPrincipal{OpenID: "ou-sender"}}}}); got != "ou-sender" {
		t.Fatalf("same actor = %q", got)
	}
	if got := batchActorOpenID(worker.Batch{Messages: []worker.Message{{Key: key, Actor: worker.ActorPrincipal{OpenID: "ou-a"}}, {Key: key, Actor: worker.ActorPrincipal{OpenID: "ou-b"}}}}); got != "" {
		t.Fatalf("mixed actors must fail closed, got %q", got)
	}
	if got := batchActorOpenID(worker.Batch{Messages: []worker.Message{{Key: key}}}); got != "" {
		t.Fatalf("missing actor must fail closed, got %q", got)
	}
}
