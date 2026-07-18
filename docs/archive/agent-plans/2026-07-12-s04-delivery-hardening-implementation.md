# S04 Delivery Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make companion delivery cancellation and terminal ownership deterministic, then synchronize S04 with the accepted full-entity CardKit implementation and close the Story delivery artifacts.

**Architecture:** `internal/worker` owns a per-batch `TerminalArbiter` and `DeliverySlot`; the worker controls all transition/cancellation operations through them. An error-returning workflow writer records segment metadata before the next segment is sent. Documentation names CardKit full entity update as the work-mode primary path and preserves PATCH only as fallback.

**Tech Stack:** Go 1.23, `slog`, MySQL lifecycle adapter, Feishu SDK, Go testing/race detector.

---

### Task 1: Terminal and delivery ownership primitives

**Files:**

- Create: `internal/worker/delivery.go`
- Create: `internal/worker/delivery_test.go`
- Modify: `internal/worker/manager.go`

- [ ] **Step 1: Write failing ownership tests**

Add tests that require `TerminalArbiter.Claim("completed")` to win once and preserve that reason, `DeliverySlot.CancelAndWait` before `Publish` to prevent publication, and cancellation after `Publish` to cancel its context and wait for `Finish`.

Run: `go test ./internal/worker -run 'TestTerminalArbiter|TestDeliverySlot' -count=1`

Expected: fail because the types do not exist.

- [ ] **Step 2: Add the minimal primitives**

Implement a mutex-protected arbiter and slot with `Begin`, `Publish`, `CancelAndWait`, `Finish`, and no exported mutable state. `Publish` derives a cancellable child context and rejects a pre-latched cancel.

Run: `go test ./internal/worker -run 'TestTerminalArbiter|TestDeliverySlot' -count=1`

Expected: pass.

- [ ] **Step 3: Bind the primitives into companion processing**

Replace direct `activeCancel` replacement during companion delivery with the slot. A durable marker calls `Begin`; the first send occurs only after `Publish`; all finalization calls `Finish`; manager cancellation delegates to the slot when present.

Run: `go test ./internal/worker -run 'TestCompanion.*Cancel|TestCancelWaitsForActiveChannelWorkerToRelease' -count=1`

Expected: pass with no visible send after pre-publication cancellation.

### Task 2: Fail-closed workflow delivery records

**Files:**

- Modify: `internal/worker/manager.go`
- Modify: `internal/worker/manager_test.go`

- [ ] **Step 1: Write a failing writer-failure test**

Inject a workflow writer that fails on the first segment record. Assert that no later segment is sent and `FailCompanionDelivery` receives `companion_delivery_trace_incomplete`.

Run: `go test ./internal/worker -run TestCompanionWorkflowWriterFailureStopsDelivery -count=1`

Expected: fail because the workflow dependency cannot return an error.

- [ ] **Step 2: Implement the writer interface and failure path**

Replace the direct logger call with an interface returning `error`. The production adapter writes `event`, `batch_id`, `source_trace_ids`, `thread_id`, `turn_id`, `segment_index`, `text_sha256`, `result`, `reason`, `message_id`, `retry_count`, and `at`. On error, claim the terminal reason, cancel/finish the slot, and use the batch failure lifecycle operation.

Run: `go test ./internal/worker -run 'TestCompanionWorkflowWriterFailureStopsDelivery|TestCompanionSendsOnlyFinalAnswerSegmentsAfterSuccessfulTerminal|TestCompanionUnknownSendStopsRemainingSegments' -count=1`

Expected: pass.

### Task 3: Accepted CardKit contract and delivery documents

**Files:**

- Modify: `docs/story/S04-双区流式卡片展示-设计.md`
- Modify: `docs/02-redesign-high-level.md`
- Modify: `README.md`
- Modify: `docs/story/STORY_LIST.md`
- Create: `docs/story/S04-全过程复盘-2026-07-12.md`

- [ ] **Step 1: Synchronize the primary CardKit contract**

Replace component-level `element_id`/stable operation requirements with a same-entity, increasing-sequence full `Card.Update(card_json)` contract. Document PATCH as per-message fallback and constrain any circuit to a recoverable scope.

- [ ] **Step 2: Record the user-approved validation boundary**

Mark successful work and companion L4 evidence, document that companion empty/failed/restart external messages are deferred by the user, and map the focused ownership tests to the delivered S04 scope.

- [ ] **Step 3: Write retrospective and sync status**

Record the CardKit component API finding, process/binary freshness rule, companion workflow ownership fix, residual risks, and the next story responsibility. Mark S04 Delivered only after fresh verification and independent blocker-only review.

### Task 4: Fresh release verification

**Files:**

- Modify: `runtime/codex_workspace_bot_s04`
- Modify: `runtime/s04.pid`
- Modify: `progress.md`

- [ ] **Step 1: Run required validation**

Run:

```bash
gofmt -w internal/worker/*.go
go test ./... -count=1
go vet ./...
go test -race ./... -count=20
go test -race ./internal/codexapp ./internal/worker ./internal/storage ./internal/output -count=50
go build -o runtime/codex_workspace_bot_s04 ./cmd/server
```

Expected: every command exits zero.

- [ ] **Step 2: Start the newly built process**

Stop the PID in `runtime/s04.pid`, start the built binary in an isolated process session, update the PID file, and confirm its executable timestamp is not newer than the process start.

Run: `curl -fsS http://127.0.0.1:8080/healthz && curl -fsS http://127.0.0.1:8080/readyz`

Expected: `ok` and three connected receivers.

- [ ] **Step 3: Obtain blocker-only independent review**

Review the diff, tests, current process, L4 evidence and documentation. Resolve every blocker before changing the Story state to Delivered.
