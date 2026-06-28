# Story 06 Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the Story 06 P0-P3 review findings for the mock-backed Go scaffold.

**Architecture:** Keep Story 06 scoped to a real local/mock scaffold, not real Feishu WebSocket or real Codex app-server. Fix runtime correctness in `session`, `task`, `approval`, `debugapi`, and `app`, then add acceptance evidence tooling and documentation.

**Tech Stack:** Go 1.23, standard library HTTP/filesystem/concurrency, GORM SQLite, YAML fixtures, shell smoke scripts.

---

### Task 1: Approval and Engine Contract Safety

**Files:**
- Modify: `internal/session/session.go`
- Modify: `internal/engine/engine.go`
- Modify: `internal/mockengine/mockengine.go`
- Test: `internal/session/session_test.go`
- Test: `internal/engine/engine_test.go`

- [ ] **Step 1: Write failing tests**

Add tests proving approval requests stop before assistant output is persisted or sent, approval scenarios have a valid terminal event, and malformed event sequences are rejected.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `go test ./internal/session ./internal/engine`

Expected: tests fail because session currently treats pending approval as completed output and does not validate event sequence.

- [ ] **Step 3: Implement minimal code**

Make `collectTurn` validate event order, require one terminal event, return a pending-approval turn without output when approval is requested, and preserve approval rows. Update mock scenarios to include valid terminal states.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/session ./internal/engine ./internal/mockengine`

Expected: focused tests pass.

### Task 2: Safe Debug Attachment Handling and Attachment Limits

**Files:**
- Modify: `internal/config/config.go`
- Modify: `config.yaml.template`
- Modify: `internal/session/session.go`
- Test: `internal/session/session_test.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write failing tests**

Add tests rejecting absolute attachment temp paths, symlink temp paths, workspace-outside temp paths, oversized attachments, and too many pending attachments.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `go test ./internal/session ./internal/config`

Expected: tests fail because only `..` segments are rejected and attachment limits are missing.

- [ ] **Step 3: Implement minimal code**

Add attachment config defaults for pending TTL, max pending items, and max bytes per attachment. Constrain temp paths to a configured workspace temp directory or the workspace root, reject symlinks, bound `io.Copy`, and keep filename sanitization.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/session ./internal/config`

Expected: focused tests pass.

### Task 3: Task Runtime Completion

**Files:**
- Modify: `internal/task/task.go`
- Modify: `internal/task/runner.go`
- Modify: `internal/task/scheduler.go`
- Create: `internal/task/watcher.go`
- Modify: `internal/app/app.go`
- Test: `internal/task/runner_test.go`
- Test: `internal/task/scheduler_test.go`
- Test: `internal/task/watcher_test.go`
- Test: `internal/app/app_test.go`

- [ ] **Step 1: Write failing tests**

Add tests proving repeated task runs are not deduplicated, missing manager returns an error, channel tasks use fresh task thread semantics, watcher scan mirrors YAML tasks into DB and scheduler, and bootstrap starts the scanner/scheduler path.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `go test ./internal/task ./internal/app ./internal/session`

Expected: tests fail because fixed task message IDs dedupe repeats, no watcher exists, and bootstrap does not start scheduling.

- [ ] **Step 3: Implement minimal code**

Generate unique task run message IDs, carry task metadata into session context, add a task thread policy path, implement filesystem scan/reconcile for YAML tasks, support scheduler add/remove/replace/cron-ish trigger state, and wire app bootstrap.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/task ./internal/app ./internal/session`

Expected: focused tests pass.

### Task 4: Cleanup, Shutdown, and Observability

**Files:**
- Modify: `internal/db/db.go`
- Modify: `internal/session/session.go`
- Modify: `internal/app/app.go`
- Modify: `cmd/server/main.go`
- Modify: `internal/observability/observability.go`
- Test: `internal/session/session_test.go`
- Test: `internal/app/app_test.go`
- Test: `cmd/server/main_test.go`
- Test: `internal/observability/observability_test.go`

- [ ] **Step 1: Write failing tests**

Add tests proving attachment cleanup respects TTL and removes files, runtime close drains managers, server shutdown calls runtime close, and app bootstrap emits structured lifecycle logs instead of defaulting to `NopEmitter`.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `go test ./internal/session ./internal/app ./cmd/server ./internal/observability`

Expected: tests fail because cleanup ignores TTL, runtime has no close, and emitter is not wired.

- [ ] **Step 3: Implement minimal code**

Add DB cleanup queries by cutoff, remove temp/session files, add `Runtime.Close`, call it from server shutdown, and pass a `SlogEmitter` in bootstrap.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/session ./internal/app ./cmd/server ./internal/observability`

Expected: focused tests pass.

### Task 5: Acceptance Evidence and Dev/Test Runbook

**Files:**
- Create: `scripts/story06_smoke.sh`
- Create: `docs/evidence/story06/README.md`
- Modify: `README.md`
- Test: `internal/artifact/artifact_test.go`

- [ ] **Step 1: Write failing tests**

Add artifact tests requiring the smoke script and evidence README to exist and mention health, debug dispatch, task run, SQLite evidence, artifact cleanliness, and debug-disabled checks.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `go test ./internal/artifact`

Expected: tests fail because the evidence script/docs do not exist.

- [ ] **Step 3: Implement minimal files**

Add a deterministic local smoke script that creates a temp config/workspace, starts the server, checks health, dispatches a message, runs a task, queries SQLite, checks debug disabled mode, records outputs, and cleans up. Document evidence capture.

- [ ] **Step 4: Verify GREEN**

Run: `go test ./internal/artifact`

Expected: focused tests pass.

### Task 6: Final Verification and Review

**Files:**
- All modified files

- [ ] **Step 1: Format**

Run: `gofmt -w` on changed Go files.

- [ ] **Step 2: Full test suite**

Run: `go test ./...`

Expected: all packages pass.

- [ ] **Step 3: Race and static checks**

Run: `go test -race ./internal/session ./internal/task ./internal/approval ./internal/db ./internal/debugapi ./internal/config ./internal/workspace ./internal/app`

Run: `go vet ./...`

Run: `go build ./...`

Expected: all commands exit 0.

- [ ] **Step 4: Smoke script**

Run: `bash scripts/story06_smoke.sh`

Expected: script exits 0 and writes evidence under `docs/evidence/story06/latest/`.

- [ ] **Step 5: Review worktree**

Run: `git status --short` and inspect changed files.

Expected: only intended Story 06 remediation files changed.
