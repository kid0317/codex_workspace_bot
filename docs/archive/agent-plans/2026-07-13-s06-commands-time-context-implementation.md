# S06 Commands and Time Context Implementation Plan

> **For Codex:** Execute this plan in the current checkout. The S01–S05 baseline is intentionally uncommitted and powers the local service; do not create a clean worktree that would omit it.

**Goal:** Deliver S06 command handling and normal-message Shanghai time context with receipt-first idempotency, Worker-owned control serialization, privacy-safe `/goal`, local tests, a fresh local runtime, and a clear L4 Feishu validation boundary.

**Architecture:** Classify input before persistence, record a command-aware receipt, then route each command through its designed boundary: direct read-only status, Worker serialized controls, Worker-exclusive goal, or a static help/error response. Keep control effects and their visible reply independently durable. Add the router receipt timestamp to ordinary worker messages; the processor renders it immediately before each opaque user body.

**Tech Stack:** Go 1.23, MySQL 8 migrations, `larksuite/oapi-sdk-go/v3`, Codex App Server JSON-RPC, Go `testing`/`sqlmock`/race detector.

---

### Task 1: Baseline and accepted-contract synchronization

**Files:**
- Modify: `docs/02-redesign-high-level.md`
- Modify: `docs/story/S06-飞书斜杠命令与时间上下文-设计.md`
- Modify: `docs/story/STORY_LIST.md`
- Modify: `README.md`, `config.yaml.template`, `.env.example`
- Modify: `task_plan.md`, `progress.md`, `findings.md`

1. Record S06 as `In Development`; replace the obsolete HLD command pseudocode with the approved five-command, status-direct, goal-serialized, receipt-first contract.
2. Add the S06 configuration/runtime contract without exposing secrets; document the final local verification command and the user L4 sequence.
3. Run `git diff --check` and inspect only the S06 documentation diff.

### Task 2: Command parser and command-aware receipts (RED → GREEN)

**Files:**
- Create: `migrations/004_s06_commands_time.sql`
- Modify: `internal/router/router.go`
- Modify: `internal/router/router_test.go`
- Modify: `internal/storage/messages.go`
- Modify: `internal/storage/messages_test.go`, `internal/storage/incoming_test.go`

1. Write parser tests for all accepted commands, `/stop`, case/outer Unicode whitespace, invalid arguments, empty goal, unknown slash command and full-width slash ordinary text. Run `go test ./internal/router -run Test.*Command -count=1` and confirm RED.
2. Write SQL mock tests asserting command receipt fields, redacted goal storage, UTC-millisecond `received_at`, payload digest/byte count and duplicate non-effects. Run `go test ./internal/storage -run 'Test.*(Command|Incoming)' -count=1` and confirm RED.
3. Add the forward-only, information-schema-safe migration and typed receipt fields. Implement pure command classification plus `PersistIncoming` fields and conditional command outcome updates.
4. Run `gofmt`, the focused router/storage tests, then `go test ./internal/router ./internal/storage -count=1`.

### Task 3: Normal-message time formatter and safe debug redaction (RED → GREEN)

**Files:**
- Modify: `internal/feishu/feishu.go`, `internal/feishu/normalize_test.go`
- Modify: `internal/router/router.go`, `internal/router/router_test.go`
- Modify: `internal/worker/manager.go`, `internal/worker/manager_test.go`
- Modify: `internal/codexapp/processor.go`, `internal/codexapp/*_test.go`
- Modify: `internal/codexapp/timeline.go`, `internal/codexapp/timeline_test.go`

1. Add failing tests for injected receipt time, per-message Shanghai RFC3339 XML, FIFO batching, hostile user text byte preservation, and the absence of formatter use on every command path.
2. Add failing timeline tests with a sentinel goal objective in RPC response/notification payloads, asserting no sentinel is written and digest/byte metadata is retained.
3. Implement `ReceivedAt` propagation and formatter; add structured goal payload redaction before raw timeline persistence.
4. Run `gofmt` plus `go test ./internal/feishu ./internal/router ./internal/worker ./internal/codexapp -count=1`.

### Task 4: Static command responder and direct `/status` (RED → GREEN)

**Files:**
- Modify: `internal/feishu/feishu.go`, `internal/feishu/*_test.go`
- Modify: `internal/router/router.go`, `internal/router/router_test.go`
- Modify: `internal/codexapp/runtime.go`, `internal/codexapp/runtime_test.go`
- Modify: `cmd/server/main.go`

1. Write fake-adapter tests for static-card size/deadline behavior, one text fallback, rejected/unknown outcomes, and duplicate suppression.
2. Write status tests for concurrent rate-limit/usage reads, partial failure, malformed/null reset values, Shanghai rendering, no account-payload leak and no Worker admission.
3. Implement a command responder and status mapper/service. Bind the App Server status reader and response adapter in the server.
4. Run `gofmt`, focused tests, and `go test ./internal/feishu ./internal/router ./internal/codexapp -count=1`.

### Task 5: Worker-owned controls and `/goal` lifecycle (RED → GREEN)

**Files:**
- Modify: `internal/worker/manager.go`, `internal/worker/manager_test.go`, `internal/worker/concurrency_test.go`
- Modify: `internal/worker/delivery.go`, `internal/worker/delivery_test.go`
- Modify: `internal/storage/messages.go`, `internal/storage/messages_test.go`
- Modify: `internal/codexapp/processor.go`, `internal/codexapp/runtime.go`, `internal/codexapp/*_test.go`
- Modify: `internal/router/router.go`, `internal/router/router_test.go`

1. Write failing tests for a control barrier, atomic detached-FIFO failure, active start-in-flight cancellation latch, 2-second normal ingress rejection, isolated second channel, and ordered double controls.
2. Write failing goal tests: it waits behind ordinary work, resumes an existing thread then calls only `thread/goal/set`, never starts a thread/turn, and reports missing/resume-failed session safely.
3. Implement `SubmitControl` and queued `Goal` as Worker-owned paths. Ensure controls wait only with bounded command contexts and persist every effect/reply outcome separately.
4. Change runtime start failure semantics so a caller cancellation does not close the shared generation; if `turn/start` binds after cancellation, issue `turn/interrupt` for that turn.
5. Run focused unit tests, `go test -race ./internal/worker ./internal/router ./internal/codexapp ./internal/storage -count=5`, and repair only demonstrated failures.

### Task 6: Integration tests, code review, migration, and fresh local runtime

**Files:**
- Modify: `internal/codexapp/l3_live_test.go` or create `internal/codexapp/s06_l3_live_test.go`
- Modify: `README.md`, S06 Story/Story List, HLD, S06 evidence/review documents as evidence becomes available

1. Add opt-in `TestS06L3RealGoalSetAndAccountStatus`, using a temporary thread and no objective plaintext in saved evidence; clean up the thread.
2. Run `gofmt -w` only on changed Go files, `go test ./... -count=1`, `go vet ./...`, and `go test -race ./... -count=1`; run `git diff --check`.
3. Perform an independent implementation review against S06-AT-00…AT-10. Fix verified blockers with new RED tests; request a blocker-only re-review before any Delivered claim.
4. Stop the old local service, build the new binary, start it with the intended config, verify `/healthz` and `/readyz`, inspect MySQL migration `004`, configuration, and sanitized runtime logs.
5. Run opt-in L3 if the local login/service permits it. Keep S06 `Ready for final local validation` until the user completes the Feishu L4 sequence; report the exact fresh PID/version and residual external gate.

## Final verification checklist

- `go test ./... -count=1`
- `go vet ./...`
- `go test -race ./... -count=1`
- `git diff --check`
- Fresh service: migration 004 recorded, health/ready successful, enabled receivers connected.
- No user goal sentinel in MySQL, logs, cards, workflow JSONL or debug timeline.

