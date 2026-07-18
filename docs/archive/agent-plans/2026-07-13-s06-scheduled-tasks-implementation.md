# S06 Scheduled Tasks and Agent Tools Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver owner-isolated, MySQL-backed Agent-managed Cron tasks that run either through the owning Channel Worker FIFO or through a fail-closed registered-script runner, then deliver safe task results.

**Architecture:** Add a dedicated `schedule` package boundary for immutable task/run state, cryptography, tool replay, due-claim/recovery, and delivery outbox. `codexapp` owns the target dynamic-tool catalog and upgrade state; `worker` owns scheduled Prompt execution and never lets it merge with ordinary messages. A bounded scheduler only discovers due rows—the transactional MySQL claim and immutable run snapshot authorize all side effects.

**Tech Stack:** Go 1.23, MySQL 8 forward-only migrations, `robfig/cron/v3`, AES-256-GCM/HMAC-SHA-256, existing Codex App Server JSON-RPC runtime, Feishu sender fakes, Go testing/sqlmock/race detector.

---

## File map

| Path | Responsibility |
|---|---|
| `migrations/005_s06_scheduled_tasks.sql` | Forward-only S06 schema, indexes, catalog/route fields and constraints. |
| `internal/config/config.go` | Schedule keyring, scheduler and Script-runner configuration validation. |
| `internal/schedule/crypto.go` | Versioned AES-GCM/HMAC protection, owner/cursor binding and redaction-safe metadata. |
| `internal/schedule/cron.go` | Strict five-field Shanghai Cron parsing and next-slot computation. |
| `internal/schedule/store.go` | Parameterized task/tool/run/delivery/registry transactions and reconciliation. |
| `internal/schedule/tools.go` | Exact-bound `list_own`, `create`, and `update` agent-tool contract. |
| `internal/schedule/scheduler.go` | Bounded due scan, atomic claim, Prompt dispatch and Script capacity. |
| `internal/schedule/script.go` | Admin registry, no-follow descriptor verification and constrained process-group runner. |
| `internal/schedule/delivery.go` | Task-result card/text outbox with rejected/unknown boundaries. |
| `internal/worker/manager.go` | Actor-preserving batches, ScheduledPrompt profile and discard/start/terminal callbacks. |
| `internal/codexapp/processor.go` | `s06-schedule-v1` catalog, persisted upgrade state and scheduled Prompt processing. |
| `cmd/server/main.go` | Startup validation, reconciliation, scheduler lifecycle, tool routing and `appctl` registration. |

## Task 1: Freeze the current S06 contract and establish the RED baseline

**Files:**
- Modify: `docs/story/STORY_LIST.md`
- Modify: `docs/story/S06-定时任务与Agent工具-设计.md`
- Modify: `docs/02-redesign-high-level.md`
- Create: `internal/schedule/contract_test.go`
- Create: `internal/schedule/cron_test.go`

- [x] Mark S06 `In Development` in the Story List; do not reactivate the superseded commands/time-context document.
- [x] Write failing tests for the accepted tool names/schema version, owner tuple, five-field Cron rejection cases, Shanghai `Next`, and the listed no-scope boundaries.
- [x] Run `go test ./internal/schedule -run 'Test(Contract|Cron)' -count=1`; confirm the failures are missing implementation rather than fixture errors.
- [x] Add the minimal S06 package constants and strict Cron parser needed to make only these tests green.
- [x] Run `gofmt -w internal/schedule` and repeat the focused test command.

## Task 2: Add fail-closed configuration and cryptographic primitives (RED → GREEN)

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`
- Modify: `config.yaml.template`, `.env.example`, `README.md`
- Create: `internal/schedule/crypto.go`, `internal/schedule/crypto_test.go`

- [x] Add failing config tests for missing/duplicate/invalid schedule payload and owner-HMAC keys, disabled Script runner, invalid privilege/limit fields, invalid Shanghai location, and default quotas/grace/tick values.
- [x] Run `go test ./internal/config -run Test.*Schedule -count=1` and confirm RED.
- [x] Implement `ScheduleConfig` with separate versioned payload and owner-HMAC keyrings. Require an active key and validate Script capability dependencies before service start; never log key material.
- [x] Write failing crypto tests for owner index isolation, AAD binding to app/group/owner/task/version/kind/field, ciphertext tamper failure, unknown key-version failure, and cursor cross-owner rejection.
- [x] Implement the minimum AES-GCM/HMAC/cursor helpers to pass. Run `gofmt` and `go test ./internal/config ./internal/schedule -count=1`.

## Task 3: Create and validate the immutable S06 migration (RED → GREEN)

**Files:**
- Create: `migrations/005_s06_scheduled_tasks.sql`
- Modify: `internal/storage/storage_test.go`

- [x] Write failing migration/schema assertions for all five S06 tables, catalog-upgrade and route fields, owner/due/unique-run indexes, and the exact delivery/tool-call uniqueness boundaries.
- [x] Run the focused migration test and confirm it fails because migration 005 is absent.
- [x] Add the information-schema-safe forward-only migration with `scheduled_tasks`, `scheduled_task_runs`, `scheduled_task_deliveries`, `scheduled_task_tool_calls`, `scheduled_script_definitions`, and the `chat_groups` additions from Story §6.1. Do not edit migration 004 or reuse its obsolete command fields.
- [x] Run migration tests, then apply migrations to an isolated local test database and inspect the recorded checksum/version.

## Task 4: Implement task, replay-ledger, claim and recovery repositories (RED → GREEN)

**Files:**
- Create: `internal/schedule/store.go`, `internal/schedule/store_test.go`
- Create: `internal/schedule/reconcile.go`, `internal/schedule/reconcile_test.go`

- [ ] Write sqlmock tests proving: parameterized owner-bound task list/create/update; optimistic version conflict; quota checks; encrypted payload-only persistence; at-most-once tool result replay; same call ID/different arguments rejection; expired in-flight rejection. (owner/task/CAS/encryption and Script snapshot coverage exist; quota and tool-call ledger cases remain.)
- [ ] Run `go test ./internal/schedule -run 'Test(Store|ToolCall)' -count=1` and confirm RED.
- [ ] Implement transaction methods that atomically couple tool-call claim, task mutation/version, and encrypted terminal result. Expose no API that returns persisted Prompt, owner ID, script ref, console, or raw Feishu identifier.
- [ ] Write failing tests for due claim races, immutable run snapshots, run-token terminal CAS, skipped misfire, `claimed` recovery, and queued/running restart reconciliation.
- [ ] Implement `ClaimDue`, run transition/heartbeat/terminal methods and reconciliation; run focused store/reconcile tests and `go test -race ./internal/schedule -run 'Test(Claim|Reconcile)' -count=10`.

## Task 5: Implement bounded owner-safe `schedule` dynamic tools (RED → GREEN)

**Files:**
- Create: `internal/schedule/tools.go`, `internal/schedule/tools_test.go`
- Modify: `internal/codexapp/processor.go`, `internal/codexapp/runtime_test.go`, `internal/codexapp/processor_test.go`
- Modify: `cmd/server/main.go`

- [ ] Write failing tests for `schedule.list_own`, `schedule.create`, and `schedule.update`: strict schemas, actor absent/mixed rejection, cross-owner isolation, cursor pagination through 101 tasks, Cron/payload validation, scripts-disabled/untrusted rejection, exact replay and version conflict. (initial actor/schema/cursor/update-kind RED→GREEN cases now live in `internal/scheduleaction`; pagination 101, replay and conflicts remain.)
- [ ] Run `go test ./internal/schedule -run TestTools -count=1` and confirm RED.
- [ ] Implement the tool handler using the active attempt's trusted `ActorPrincipal`, never arguments, as owner. Return only the documented safe metadata.
- [ ] Write failing Codex protocol tests requiring the complete existing `feishu` catalog plus the three `schedule` tools at catalog version `s06-schedule-v1` on new target threads.
- [ ] Implement catalog construction and server routing. Run `go test ./internal/schedule ./internal/codexapp -count=1`.

## Task 6: Make catalog upgrades crash-safe and Actor batches exclusive (RED → GREEN)

**Files:**
- Modify: `internal/storage/messages.go`, `internal/storage/messages_test.go`
- Modify: `internal/worker/manager.go`, `internal/worker/manager_test.go`, `internal/worker/concurrency_test.go`
- Modify: `internal/codexapp/processor.go`, `internal/codexapp/processor_test.go`

- [ ] Write failing tests for persisted `archive_pending → start_pending → stable`, archive/start crash points, old-catalog resume prohibition, orphan non-resume, and CAS conflicts.
- [ ] Write failing Worker tests proving ordinary messages only merge with the same `ActorPrincipal`; actor changes and ScheduledPrompt always form an exclusive FIFO boundary; control/shutdown discard conditionally ends scheduled runs.
- [ ] Run the targeted codexapp/worker/storage tests and confirm RED.
- [ ] Implement the persisted catalog state machine and `ActorPrincipal`/`ScheduledRun` worker fields. Ensure scheduled Prompt uses the original owner route but has no normal `messages` lifecycle/card ownership.
- [ ] Run `go test -race ./internal/codexapp ./internal/worker ./internal/storage -count=1`.

## Task 7: Dispatch and execute scheduled Prompt runs through the owning Worker (RED → GREEN)

**Files:**
- Create: `internal/schedule/scheduler.go`, `internal/schedule/scheduler_test.go`
- Modify: `internal/codexapp/processor.go`, `internal/codexapp/processor_test.go`
- Modify: `cmd/server/main.go`

- [ ] Write failing deterministic-clock tests for bounded due scan, enabled App/route recheck, one ClaimDue winner, queue/pool rejection, tick budget, no direct Processor bypass, actor-preserving FIFO, and no historical catch-up burst.
- [ ] Run `go test ./internal/schedule -run TestScheduler -count=1` and confirm RED.
- [ ] Implement a cancellable ticker whose only authority is the MySQL transaction. Dispatch Prompt snapshots using `worker.Manager.Accept`; use worker callbacks to transition queued/running/terminal with the claim token.
- [ ] Add failing processor tests that use only the decrypted run snapshot as input, bind resulting thread/turn IDs, suppress normal S04 output for the scheduled profile, and preserve dynamic-tool owner route.
- [ ] Implement the scheduled prompt processor path and run `go test -race ./internal/schedule ./internal/codexapp ./internal/worker -count=1`.

## Task 8: Add administrator-only Script registry and constrained runner (RED → GREEN)

**Files:**
- Create: `internal/schedule/script.go`, `internal/schedule/script_test.go`
- Create: `cmd/appctl/main.go`, `cmd/appctl/main_test.go`
- Modify: `cmd/server/main.go`, `README.md`, `config.yaml.template`

- [ ] Write failing tests for `appctl schedule register-script`: app workspace containment, relative canonical ref, regular file/no-follow descriptor, descriptor HMAC, and no agent-facing registry mutation.
- [ ] Write failing runner tests for unregistered/cross-App/revoked/digest-changed/symlink-replaced descriptors; empty environment; configured uid/gid; process-group timeout/cancel/reap; output caps and UTF-8-safe truncation.
- [ ] Run `go test ./internal/schedule ./cmd/appctl -run 'Test(Register|Runner)' -count=1` and confirm RED.
- [ ] Implement descriptor registration, snapshot verification, and a fixed-shell low-privilege runner. Startup must fail closed when Script capability is enabled without the validated platform prerequisites; never persist console or script refs.
- [ ] Run targeted tests and `go test -race ./internal/schedule ./cmd/appctl -count=1`.

## Task 9: Deliver safe Prompt/Script results through an outbox (RED → GREEN)

**Files:**
- Create: `internal/schedule/delivery.go`, `internal/schedule/delivery_test.go`
- Modify: `internal/feishu/feishu.go`, `internal/feishu/feishu_test.go`
- Modify: `cmd/server/main.go`

- [ ] Write failing tests for static non-silent Prompt/Script result cards, silent `suppressed` intents, no running card, primary `rejected` one-time fallback, `unknown` no-replay, and delivery/run token conditional state transitions.
- [ ] Add failing redaction tests that prohibit Prompt, script ref, owner/open ID, raw Feishu IDs, and Script console outside the sole non-silent Script card; verify console escaping/size/exit/truncation metadata.
- [ ] Run `go test ./internal/schedule ./internal/feishu -run 'Test(Delivery|Redaction)' -count=1` and confirm RED.
- [ ] Implement outbox claim/finalization and adapter calls. Reconcile stale in-flight delivery to `unknown`, never resend it, and only create a text fallback after explicit card rejection.
- [ ] Run focused tests and `go test -race ./internal/schedule ./internal/feishu -count=1`.

## Task 10: Runtime lifecycle, L3 proof and independent implementation review

**Files:**
- Create: `internal/codexapp/s06_l3_live_test.go`
- Create: `scripts/story06_smoke.sh`
- Create: `docs/evidence/story06/latest/.gitkeep`
- Create: `docs/story/reviews/S06-实现独立评审-YYYY-MM-DD.md`
- Modify: `docs/story/S06-定时任务与Agent工具-设计.md`, `docs/story/STORY_LIST.md`, `docs/02-redesign-high-level.md`, `README.md`

- [ ] Add opt-in real App Server L3 tests for `s06-schedule-v1` catalog, an exact `item/tool/call` round-trip, target-thread upgrade/resume behavior, and no sensitive plaintext in saved evidence.
- [ ] Create a smoke script that uses a dedicated test database/config, applies migration 005, performs deterministic claim/restart checks and emits only digests/metadata.
- [ ] Run fresh `gofmt`, `go test ./... -count=1`, `go vet ./...`, `go test -race ./... -count=1`, `go build ./cmd/server ./cmd/appctl`, the S06 smoke script, `git diff --check`, and opt-in L3 when locally available.
- [ ] Review the implementation line-by-line against S06-AT-00 through S06-AT-12. Fix every verified blocker through a new RED test and obtain a separate blocker-only re-review; then synchronize Story/HLD/README and record residual L4 scope.

## Task 11: Apply, restart, read back and hold the L4 boundary

**Files:**
- Modify: `docs/story/S06-定时任务与Agent工具-设计.md`
- Modify: `docs/story/STORY_LIST.md`
- Create: `docs/evidence/story06/latest/operator-summary.md`

- [ ] Stop the old process, apply migration 005 to the configured Docker MySQL, register the harmless test Script via `appctl`, build and start the intended binary/config.
- [ ] Verify the new process start time/PID/version, `/healthz`, `/readyz`, migration checksum, active Schedule config, scheduler reconciliation records, and redacted logs. Do not ask for validation against an older process.
- [ ] Run the Story §8 local sequence as far as local dependencies permit. Keep the Story `Ready for final local validation` until the user verifies a new non-silent Prompt card and a new non-silent Script card in Feishu; retain the exact fresh trace/run/delivery evidence needed for that check.

## Completion gate

- [ ] Every S06-AT-00…AT-12 has mapped fresh evidence; L1/L2/L3/L4 are not conflated.
- [ ] `gofmt`, `go vet ./...`, `go test ./... -count=1`, target and full `go test -race`, build, migration/read-back, health/ready and redaction checks are fresh.
- [ ] Independent implementation review and blocker-only re-review contain no unaddressed blocker.
- [ ] Service is the freshly built configuration/database generation and remains running for the user’s Feishu L4.
