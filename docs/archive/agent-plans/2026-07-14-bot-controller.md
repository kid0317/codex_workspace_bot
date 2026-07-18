# Bot Controller Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a safe root-level controller for building and gracefully managing the local Codex Workspace Bot.

**Architecture:** The Bash controller owns one transient user-systemd unit and one stable runtime binary. Before resource teardown, the Go service invokes an explicit worker-manager shutdown that follows the `/cancel` control path for every active Turn and waits for its App Server interrupt; only the unit cgroup or pre-recorded descendants are eligible for forced cleanup.

**Tech Stack:** Bash, systemd user units, Go build, curl, procfs.

---

### Task 1: Bot-wide active-Turn interrupt

**Files:**
- Modify: `internal/worker/manager.go`
- Modify: `internal/worker/manager_test.go`
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Write a failing test for manager-wide shutdown**

```go
func TestManagerShutdownCancelsActiveTurnAndWaits(t *testing.T) {
    // Start one blocking processor, call Shutdown, and assert its context is
    // cancelled before Shutdown returns.
}
```

- [ ] **Step 2: Run the targeted test and verify it fails because `Shutdown` is absent**

Run: `go test ./internal/worker -run TestManagerShutdownCancelsActiveTurnAndWaits -count=1`

Expected: FAIL with `manager.Shutdown undefined`.

- [ ] **Step 3: Implement `Manager.Shutdown(ctx)` and call it after SIGTERM**

```go
shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
if err := manager.Shutdown(shutdownCtx); err != nil {
    slog.Warn("worker_shutdown", "event", "worker_shutdown_incomplete", "error", err)
}
```

- [ ] **Step 4: Verify the targeted test and existing interrupt regression pass**

Run: `go test ./internal/worker ./internal/codexapp -run 'TestManagerShutdownCancelsActiveTurnAndWaits|TestStartTurn.*Interrupt' -count=1`

Expected: PASS and the runtime regression observes `turn/interrupt`.

### Task 2: Controller contract test

**Files:**
- Create: `scripts/test_bot_controller.sh`
- Test: `scripts/test_bot_controller.sh`

- [ ] **Step 1: Write a failing test for the supported command contract**

```bash
assert_fails ./bot_controller.sh
assert_fails ./bot_controller.sh unknown
assert_output_contains ./bot_controller.sh build 'go build'
```

- [ ] **Step 2: Run the test to verify it fails because the controller is absent**

Run: `bash scripts/test_bot_controller.sh`

Expected: FAIL reporting that `bot_controller.sh` is missing.

### Task 3: Implement and validate the controller

**Files:**
- Create: `bot_controller.sh`
- Modify: `scripts/test_bot_controller.sh`

- [ ] **Step 1: Add the minimal command parser and build implementation**

```bash
case "${1:-}" in
  build) build ;;
  start) start ;;
  stop) stop ;;
  restart) stop && start ;;
  *) usage; exit 2 ;;
esac
```

- [ ] **Step 2: Add systemd start/stop helpers with `KillMode=mixed` and `TimeoutStopSec=45`**

```bash
systemd-run --user --unit=codex-workspace-bot --collect \
  --property=KillMode=mixed --property=TimeoutStopSec=45 \
  --property="WorkingDirectory=$ROOT" ...
```

- [ ] **Step 3: Add repository-scoped residual process discovery and descendant-only cleanup**

```bash
readlink -f "/proc/$pid/exe" | grep -Eq "^$ROOT/runtime/codex_workspace_bot(_s[0-9]+)?( \(deleted\))?$"
```

- [ ] **Step 4: Run contract tests, syntax checks, and a live restart**

Run: `bash scripts/test_bot_controller.sh && bash -n bot_controller.sh && ./bot_controller.sh build && ./bot_controller.sh restart`

Expected: all checks pass; `/healthz` is `ok` and `/readyz` has only connected receivers.
