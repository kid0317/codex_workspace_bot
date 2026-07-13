package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

func TestManagerProcessesDifferentChannelKeysConcurrently(t *testing.T) {
	output := &fakeOutput{}
	started := make(chan worker.Key, 2)
	release := make(chan struct{})
	manager := worker.NewManager(worker.Config{}, func(worker.Batch) (worker.Output, error) { return output, nil }, func(ctx context.Context, batch worker.Batch) (worker.ProcessResult, error) {
		started <- batch.Key
		select {
		case <-release:
			return worker.ProcessResult{}, nil
		case <-ctx.Done():
			return worker.ProcessResult{}, ctx.Err()
		}
	})
	defer manager.Close()
	for _, key := range []worker.Key{worker.P2PKey("ou-a", "app-a"), worker.GroupKey("oc-b", "app-a")} {
		if err := manager.Accept(context.Background(), testMessage(key, key.Peer, "message")); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	deadline := time.After(time.Second)
	for len(seen) < 2 {
		select {
		case key := <-started:
			seen[key.String()] = true
		case <-deadline:
			t.Fatalf("started keys = %#v", seen)
		}
	}
	close(release)
}
