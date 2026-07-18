# S04 Streaming Output Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver S04 work-mode dual-zone streaming cards and companion final-only segmented delivery, including persistence, bounded event routing, tests, a restarted local server and a ready-to-test Feishu instance.

**Architecture:** Add a mode-aware presentation sink between the App Server runtime and the channel Worker. The runtime accepts only bound `item/completed.agentMessage` candidates and sends bounded presentation events to the Worker; the Worker owns projection/delivery state, output calls and terminal draining. Storage owns atomic batch state transitions and delivery markers; the Feishu adapter owns CardKit/PATCH/text transport and typed outcomes.

**Tech Stack:** Go 1.23, MySQL 8, larksuite/oapi-sdk-go/v3, Codex App Server JSON-RPC, Go testing/race detector.

---

### Task 1: Baseline and configuration/migration contract

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`, `config.yaml.template`
- Create: `migrations/002_s04_companion_delivery.sql`
- Modify: `internal/storage/storage.go`, `internal/storage/storage_test.go`

- [ ] Write failing config and migration tests for `streaming.companion_segment_delay_ms` default 400, range validation, and migration idempotence.
- [ ] Run `go test ./internal/config ./internal/storage -run 'S04|Streaming|Migration' -count=1` and confirm RED.
- [ ] Implement the streaming config schema/default/validation and forward-only migration runner support.
- [ ] Re-run target tests, then `gofmt` changed Go files.

### Task 2: Presentation mapping and companion lexer/segmenter

**Files:**
- Create: `internal/output/presentation.go`, `internal/output/presentation_test.go`
- Create: `internal/output/companion.go`, `internal/output/companion_test.go`

- [ ] Write failing table tests for the allowlist, sanitizer, latest-final selection, delimiter variants, protected code, escaping, marker-only final and deterministic segmentation/delay injection.
- [ ] Run `go test ./internal/output -run 'Test.*S04|TestCompanion|TestPresentation' -count=1` and confirm RED.
- [ ] Implement pure mapper/projection/lexer/segmenter functions with no Feishu or MySQL dependency.
- [ ] Re-run target tests and `go test -race ./internal/output -count=50`.

### Task 3: Runtime-to-Worker bounded event contract

**Files:**
- Modify: `internal/codexapp/runtime.go`, `internal/codexapp/processor.go`, corresponding tests
- Modify: `internal/worker/manager.go`, corresponding tests

- [ ] Write failing fake-App-Server tests for bound-only Item delivery, pre/post-bind count+byte limits, early terminal ordering, duplicate/late rejection and terminal drain acknowledgement.
- [ ] Run focused `codexapp`/`worker` tests to confirm RED.
- [ ] Implement attempt-owned event publication, the bounded reservation and Worker-owned TerminalArbiter/DeliverySlot handoff.
- [ ] Re-run focused tests with `-race -count=50`.

### Task 4: Storage finalization and companion delivery semantics

**Files:**
- Modify: `internal/storage/messages.go`, `internal/storage/messages_test.go`
- Modify: `internal/worker/manager.go`, `internal/worker/manager_test.go`
- Modify: `internal/logging/manager.go`, tests as required

- [ ] Write failing transaction tests for MarkCompanionDeliveryStarted, CompleteBatch, FailCompanionDelivery, transient transaction retry and restart-abandoned selection.
- [ ] Write failing Worker tests for no pre-terminal companion output, control-before-publish SendText=0, unknown/rejected/429 ordering, trace-writer failure and no visible resend on DB retry.
- [ ] Implement minimal storage interfaces/transactions, typed output outcomes and workflow trace writer; then implement the Worker delivery state machine.
- [ ] Run storage/worker/logging target tests and race tests.

### Task 5: Feishu CardKit/PATCH output adapter and end-to-end fake scenarios

**Files:**
- Modify: `internal/feishu/feishu.go`, `internal/feishu/*_test.go`
- Modify: `cmd/server/main.go` and configuration wiring as required

- [ ] Write failing adapter tests for dual-zone Card JSON, same-message PATCH fallback, byte budgets and final/terminal update ordering.
- [ ] Implement CardKit preflight transport where SDK supports it; retain same-message PATCH as deterministic fallback.
- [ ] Run fake L1/L2 S04-AT-00 through AT-24 coverage and full race suite.

### Task 6: Independent review, live application and acceptance preparation

**Files:**
- Modify: `README.md`, `docs/story/S04-双区流式卡片展示-设计.md`, `docs/story/STORY_LIST.md`, `progress.md`
- Create: `docs/story/reviews/S04-实现独立复审-2026-07-12.md`

- [ ] Run independent implementation review and resolve all blocker/important findings.
- [ ] Build the server, stop the old process, apply migration, confirm configuration/database state, start the new binary and verify health/ready plus fresh process/log evidence.
- [ ] Run CardKit L3/L3b if credentials/permissions allow; otherwise record the exact external gate and retain PATCH mode.
- [ ] Leave the fresh service running and provide the unique Feishu smoke instruction for the user; after user input, inspect logs/DB/trace and complete L4/retrospective before marking Delivered.

## Plan self-review

- Scope coverage: Tasks 1–5 map to S04 AT-00 through AT-24 and the runtime, storage, output and Feishu contracts; Task 6 maps to the SOP runtime and human-validation gates.
- No placeholders: each task names its files, expected RED command and verification boundary.
- Boundary consistency: `codexapp` publishes presentation events, `worker` owns ordering/delivery, `output` is pure mapping plus Feishu adapter, and `storage` owns conditional transactions.
