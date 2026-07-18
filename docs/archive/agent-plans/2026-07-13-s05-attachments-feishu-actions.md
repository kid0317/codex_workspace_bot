# S05 Attachments and Feishu Actions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver image/file ingress, session-scoped local attachment materialization, and Codex dynamic-tool Feishu actions for the current conversation.

**Architecture:** `feishu` parses only attachment metadata; `router` writes the receipt and attachment staging rows before enqueue; the owning `worker` materializes attachments before one attachment-bound Turn. `codexapp` owns dynamic-tool JSON-RPC routing and feeds an attempt-bound action executor; a per-App `feishu` adapter owns all external API calls while `storage` owns idempotent action/attachment transitions.

**Tech Stack:** Go 1.23, MySQL migrations, larksuite Go SDK, Codex App Server JSON-RPC, Go `testing`/sqlmock, Docker MySQL and explicit real App Server/Feishu L3/L4 tests.

---

### Task 1: S05 configuration, crypto keyrings and migration

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`, `config.yaml.template`
- Create: `migrations/003_s05_attachments_actions.sql`
- Modify: `internal/storage/storage.go`, `internal/storage/storage_test.go`

- [ ] Write failing config tests for required base64 32-byte attachment/action keys, workspace-relative attachment root, 30,000,000-byte defaults and retention bounds.
- [ ] Run `go test ./internal/config -run S05 -count=1` and confirm RED.
- [ ] Implement `AttachmentsConfig`, versioned keyring validation and action config; add forward-only migration for `attachments`, `feishu_action_calls`, and `chat_groups.codex_toolset_version`.
- [ ] Run `gofmt -w internal/config`, `go test ./internal/config -run S05 -count=1`, then migration idempotence tests.

### Task 2: Attachment receipt storage and lifecycle

**Files:**
- Create: `internal/storage/attachments.go`, `internal/storage/attachments_test.go`
- Modify: `internal/storage/messages.go`, `internal/storage/incoming_test.go`, `internal/router/router.go`, `internal/router/router_test.go`

- [ ] Write failing tests for transactional receipt+attachment staging, malformed attachment durable failure, duplicate no-op, conditional processing lease, ready/fail transitions and action-claim digest conflict.
- [ ] Run targeted storage/router tests and confirm each new behavior fails before implementation.
- [ ] Implement typed attachment/action records and conditional SQL transitions; extend router store contract to persist a typed `Incoming` once before queueing.
- [ ] Re-run targeted tests, `go test -race ./internal/storage ./internal/router -count=20`, and `gofmt` changed Go files.

### Task 3: Feishu attachment parsing and materialization

**Files:**
- Modify: `internal/feishu/feishu.go`, `internal/feishu/normalize_test.go`
- Create: `internal/attachment/processor.go`, `internal/attachment/processor_test.go`
- Modify: `internal/router/router.go`, `internal/worker/manager.go`, `internal/worker/manager_test.go`

- [ ] Write failing tests for image/file content parsing, attachment-only input, invalid-resource receipt, attachment Batch boundaries and image/text FIFO isolation.
- [ ] Write failing materializer tests for stream `max+1`, `.part` removal, SHA-256, image decode, canonical session path, failure cleanup and manifest construction.
- [ ] Implement parser-only WebSocket ingress and a Worker-owned materializer with a Feishu resource downloader interface; no receiver callback may download a resource.
- [ ] Re-run `go test ./internal/feishu ./internal/attachment ./internal/router ./internal/worker -count=1`, then targeted `-race` tests.

### Task 4: Codex typed attachment inputs and dynamic-tool protocol router

**Files:**
- Modify: `internal/codexapp/client.go`, `internal/codexapp/client_test.go`, `internal/codexapp/runtime.go`, `internal/codexapp/runtime_test.go`, `internal/codexapp/processor.go`
- Create: `internal/codexapp/tools.go`, `internal/codexapp/tools_test.go`, `internal/codexapp/s05_l3_live_test.go`

- [ ] Write failing fake-stdio tests for `thread/start.dynamicTools`, pre-bind `item/tool/call` buffering, exact thread+turn routing, one response per server request, unknown request rejection and reader non-blocking behavior.
- [ ] Write failing processor tests for localImage plus ordinary-file manifest inputs, old-thread toolset upgrade and resume persistence metadata.
- [ ] Implement explicit protocol request/response types, single writer server-request responses, attempt-owned bounded action executor and dynamic `feishu` namespace registration.
- [ ] Re-run `go test ./internal/codexapp -count=1` and `go test -race ./internal/codexapp -count=20`; run explicit real L3 regression guarded by `S05_RUN_REAL_APP_SERVER=1`.

### Task 5: Current-channel Feishu action proxy

**Files:**
- Create: `internal/feishuaction/service.go`, `internal/feishuaction/service_test.go`
- Modify: `internal/feishu/feishu.go`, `cmd/server/main.go`, `internal/storage/attachments.go`

- [ ] Write failing fake-client tests for current p2p/group text targets, arbitrary local regular-file upload+send, 30 MB rejection, doc creation/announcement, claim replay, 429 rejection and unknown outcome without retry.
- [ ] Implement `feishu.message_send_current_channel`, `feishu.file_upload_and_send_current_channel`, and `feishu.doc_create_and_announce`; bind every call to app/channel/thread/turn/call metadata and return redacted dynamic-tool results.
- [ ] Re-run focused package tests, `go test -race ./internal/feishu ./internal/feishuaction ./internal/storage -count=20`, and run `gofmt`.

### Task 6: Cleanup, startup wiring and documentation

**Files:**
- Create: `internal/attachment/cleanup.go`, `internal/attachment/cleanup_test.go`
- Modify: `cmd/server/main.go`, `README.md`, `docs/02-redesign-high-level.md`, `docs/story/S05-附件输入与飞书能力代理-设计.md`, `docs/story/STORY_LIST.md`

- [ ] Write failing tests for expired terminal attachment deletion, active lease preservation and restart reconciliation of staged/processing rows.
- [ ] Implement startup reconciliation, bounded cleanup scheduling and app-scoped materializer/action wiring; keep all credentials in process memory only.
- [ ] Sync README/HLD/Story contracts and mark S05 `In Development` while evidence is incomplete.
- [ ] Run `go vet ./...`, `go test ./...`, `go test -race ./...`, and inspect redacted logs.

### Task 7: Independent review, migration, restart and real acceptance preparation

**Files:**
- Create: `docs/story/reviews/S05-实现独立复审-2026-07-13.md`
- Modify: `README.md`, `docs/story/S05-附件输入与飞书能力代理-设计.md`, `docs/story/STORY_LIST.md`

- [ ] Perform an independent technical, quality/security and product review; repair every blocker and obtain blocker-only re-review.
- [ ] Stop the old service, apply migration, verify configured keys/paths and enabled Apps, start the new binary, then capture fresh `/healthz` and `/readyz` evidence.
- [ ] Execute S05-LI-01 with the configured test App when the operator sends the unique image/file messages; save only redacted trace/message/document/action/DB evidence.
- [ ] Mark `Ready for final local validation` until the user’s real Feishu test completes; only then decide Delivered and write the mandatory retrospective.

## Current execution state (2026-07-13)

- [x] Tasks 1–6 are implemented in the current checkout: MySQL migration, encrypted attachment/action state, worker-owned materialization, dynamic tools, current-channel action proxy, restart reconciliation and retention cleanup are present.
- [x] The action ledger now has the explicit `claimed -> in_flight -> terminal` boundary: validation/cancellation can finish a claimed action without an external request, and only an atomic `StartAction` transition permits Feishu I/O.
- [x] Current local gates passed after the last change: `go test ./... -count=1`, `go test -race ./... -count=1`, `go vet ./...`, and `go build -o runtime/codex_workspace_bot_s05 ./cmd/server`.
- [x] The guarded real App Server L3 smoke passed on this host: `S03_RUN_REAL_APP_SERVER=1 go test ./internal/codexapp -run '^TestL3RealAppServerTurn$' -count=1 -v`.
- [x] The local service was restarted after that build; `/healthz` is `ok`, all configured Apps are `connected` in `/readyz`, and migration `003_s05_attachments_actions.sql` is recorded.
- [ ] S05-LI-01 is partially complete: a fresh file ingress on the restarted process succeeded for App `aipm` with workspace `/root/aipm-codex`; redacted evidence is in `docs/story/S05-本地集成验证-2026-07-13.md`. Real image ingress and the current-channel text/file/document actions remain.
- [ ] An implementation-review artifact and the Delivered retrospective remain blocked on that external acceptance; S05 stays `In Development` until then.

## Plan self-review

- Coverage: Tasks 1–6 map to S05 AT-00 through AT-11; Task 7 maps to the SOP runtime, external-boundary and Delivered gates.
- Scope: current-channel actions, arbitrary ordinary file upload and no local limiter are explicit; cross-chat routing, rich-text/media and approval cards remain out of scope.
- Protocol: Task 4 preserves the verified dynamic-tool thread/resume contract with fake and real L3 regression coverage.
