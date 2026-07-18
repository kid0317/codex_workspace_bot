# S07 Goal Continuation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `/goal <objective>` immediately start Codex work and retain the channel/output until the authoritative Goal terminal state.

**Architecture:** Route a Goal as an exclusive Worker batch instead of an out-of-band callback. Before `goal/set`, prepare delivery and pre-register a thread-level GoalAttempt/redaction owner. Processor ensures a thread, sets the persisted active Goal, then starts the Goal-aware runtime attempt with the objective as the first input. Runtime keeps that attempt attached across continuation turn IDs, rejects stale generation events with a terminal fence, and terminates only on goal status or an explicit local failure/control path.

**Tech Stack:** Go 1.23, existing Worker/Router/Feishu CardKit, Codex App Server JSON-RPC, Go testing/race detector.

---

### Task 1: Lock the goal event contract in runtime tests

**Files:**
- Modify: `internal/codexapp/runtime_test.go`
- Modify: `internal/codexapp/runtime_internal_test.go`
- Modify: `internal/codexapp/runtime.go`

- [ ] **Step 1: Write failing runtime state-machine tests**

Add fake sequences for: normal continuation; `turn/started` before delayed first `turn/start` response; terminal update before/after a turn completion; duplicate/old-generation events; no continuation for one idle timeout; and process exit while awaiting. Assert `StartGoal` has not returned after `turn-1`, returns only after the terminal sequence, and receives the visible item from turn 2. Assert silent continuation produces exactly one pause and `goal_continuation_suppressed`.

```go
started, err := runtime.StartGoal(ctx, "thread-goal", params)
if err != nil || started.IsZero() { t.Fatal(err) }
if got := strings.Join(items, "|"); got != "second visible output" { t.Fatal(got) }
```

- [ ] **Step 2: Run the targeted RED gate**

Run: `go test ./internal/codexapp -run TestRuntimeGoalKeepsAttemptAcrossContinuationTurns -count=1`

Expected: FAIL because no `StartGoal` API exists and/or the first `turn/completed` ends the attempt.

- [ ] **Step 3: Implement the fenced GoalAttempt routing**

Add a Goal flag/state to the existing attempt, `StartGoal`, thread-level pre-registration, current/known turn set, event sequence/generation checks, terminal/stopping fences, event decoding for `thread/goal/updated`, rebind on continuation `turn/started`, per-turn timeout reset, and one shared unregister path. Keep ordinary `StartTurn` behavior unchanged. Only `complete`, `paused`, and `budget_limited` may end a Goal attempt from a goal-status event.

- [ ] **Step 4: Run the targeted GREEN and race gates**

Run: `go test ./internal/codexapp -run 'TestRuntimeGoal|TestBindAttempt|TestRuntime' -count=1 && go test -race ./internal/codexapp -count=1`

Expected: PASS.

### Task 2: Make Goal a normal exclusive Worker batch and start it in Processor

**Files:**
- Modify: `internal/worker/manager.go`
- Modify: `internal/worker/manager_test.go`
- Modify: `internal/codexapp/processor.go`
- Modify: `internal/codexapp/runtime_test.go`

- [ ] **Step 1: Write failing Processor and Worker tests**

Replace the old “goal without starting turn” test with a test that expects delivery readiness, `thread/resume` or `thread/start`, pre-registration, `thread/goal/set(active)`, then `turn/start` whose only text input is `sentinel-goal`. Add tests for `goal_set_pending_first_turn` compensation (one pause on start/delivery/process failure), including `turn/start` accepted but response lost plus pause response lost: owner/fence remains and no ordinary Turn begins until paused/terminal or generation exit. Add control-ordering tests (stopping fence → pause confirmation → interrupt/drain → archive), and a Manager test that queues ordinary A, Goal, ordinary B and asserts A completes before Goal and B does not begin until the Goal terminal event.

```go
if request.Method != "turn/start" || params.Input[0].Text != "sentinel-goal" { t.Fatal(...) }
if got := order; !reflect.DeepEqual(got, []string{"A", "goal", "B"}) { t.Fatal(got) }
```

- [ ] **Step 2: Run the targeted RED gate**

Run: `go test ./internal/codexapp ./internal/worker -run 'TestProcessorGoal|TestManagerGoal' -count=1`

Expected: FAIL because Goal is an out-of-band callback and never creates a work batch or `turn/start`.

- [ ] **Step 3: Implement exclusive Goal batches and ProcessGoal**

Represent the objective only in memory on `worker.Message`/`Batch`; make it an exclusive queue boundary. Reuse normal work/companion output lifecycle, but branch Processor to ensure/resume/start a thread, create delivery before set, set active Goal, and call `Runtime.StartGoal` with the raw objective. On cancellation/timeout, apply stopping fence and confirm pause before interrupting/draining; if pause/get is unknown, retain the worker ownership and redaction registry fail-closed until confirmation or generation exit. On pre-first-turn failure compensate exactly once. Companion accumulates across turns and releases DeliverySlot only after Goal terminal. Preserve the normal `<now>` formatter only for non-Goal batches.

- [ ] **Step 4: Run GREEN/race gates**

Run: `go test ./internal/codexapp ./internal/worker -count=1 && go test -race ./internal/codexapp ./internal/worker -count=1`

Expected: PASS.

### Task 3: Route `/goal` through the Worker and remove static success delivery

**Files:**
- Modify: `internal/router/router.go`
- Modify: `internal/router/router_test.go`
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Write failing Router tests**

Assert a `/goal sentinel` receipt stores `"/goal [redacted]"`, enters `Manager.Accept` as one exclusive Goal message, and does not invoke `SendStaticCard("目标已设置。")`. Assert queue rejection produces one safe failure response and a no-thread `/goal` reaches the Processor start path rather than `goal_session_unavailable`.

- [ ] **Step 2: Run the RED gate**

Run: `go test ./internal/router -run 'Test.*Goal' -count=1`

Expected: FAIL because Router uses `SubmitGoal` callback and sends the static confirmation.

- [ ] **Step 3: Implement routing and remove obsolete wiring**

Build the in-memory Goal worker message from the redacted receipt’s original command argument, dispatch it via the normal Manager, remove `goalSetter`/`SubmitGoal` callback wiring, and retain only deterministic failure delivery for admission/runtime failures. Remove the obsolete server setup call.

- [ ] **Step 4: Run GREEN gates**

Run: `go test ./internal/router ./cmd/server -count=1`

Expected: PASS.

### Task 4: Redaction, documentation, and release evidence

**Files:**
- Modify: `internal/codexapp/timeline.go`
- Modify: `internal/codexapp/timeline_test.go`
- Modify: `docs/story/S07-Goal持续执行与终态展示-设计.md`
- Modify: `docs/01-codex-appserver-protocol-research.md`
- Modify: `docs/02-redesign-high-level.md`

- [ ] **Step 1: Write failing redaction tests**

Use `sentinel-goal-secret` in actual JSON-RPC `params/result.goal.objective` and first Goal `userMessage.content[].text` fixtures. Assert raw/index/outcome files retain correlation/status and digest/byte length but never the sentinel. Assert unparseable/unknown Goal-shaped raw envelopes are not written.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/codexapp -run TestTimeline.*Goal -count=1`

Expected: FAIL if any timeline surface serializes the objective.

- [ ] **Step 3: Implement the structured redactor and sync documents**

Install the structured sanitizer at the sole pre-`RecordRaw` boundary. It uses the pre-registered thread→Goal registry, redacts `params/result.goal.objective` and the identifiable first Goal input before raw persistence while preserving method, thread/turn IDs, status and size/digest metadata; unknown Goal-shaped envelopes fail closed. Add projection tests that inject reasoning, user input, plan text, command/cwd/query, tool/MCP args/results and verify absence from card, fallback, MySQL, and timeline index. Update S07 evidence links/decisions only if implementation reveals an actual protocol difference.

- [ ] **Step 4: Run final local gates and restart**

Run: `gofmt -w internal/codexapp internal/worker internal/router cmd/server && go vet ./... && go test ./... -count=1 && go test -race ./... -count=1 && go build -o runtime/codex_workspace_bot_s07 ./cmd/server && git diff --check`

Then stop the old local bot, start the new binary with `config.yaml`, verify `/healthz`, `/readyz`, migration state and new PID/start time, and retain it for S07-LI-01.
