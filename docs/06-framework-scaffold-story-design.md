# Framework Scaffold Story Design

Date: 2026-06-28

## Story Goal

Build the first runnable Go framework for `codex_workspace_bot` from the
existing `cc_workspace_bot` behavior.

This story migrates and rewrites every non-core-engine capability from
`cc_workspace_bot`. The real Codex app-server client is intentionally separated
behind a stable app-server design boundary. The first runnable scaffold uses a
deterministic mock engine, but all orchestration, state, compatibility, task,
attachment, session, output, guardrail, and verification behavior must be real.

The result must be independently testable with automated fixtures and manually
verifiable without connecting to Feishu or a real app-server.

## Non-Goals

- Do not implement the real Codex app-server protocol client in this story.
- Do not copy AIPM's FC/NAS three-state sandbox topology.
- Do not mutate existing workspace `CLAUDE.md`, existing `AGENTS.md`, or
  secret-bearing files.
- Do not redesign the task YAML contract.
- Do not require existing workspaces to be manually rewritten before first run.
- Do not depend on hook behavior for output filtering or approval decisions.

## Source Behavior To Preserve

| Area | Required migration behavior | Mock allowed |
|---|---|---|
| Multi-app config | Load legacy `config.yaml` app list, workspace modes, providers, app-level `claude` block, server/session/cleanup defaults | no |
| Workspace init | Preserve workspace root, `CLAUDE.md`, `AGENTS.md`, `.claude/skills`, `.claude/story-state-*`, `memory`, `tasks`, `sessions`, `bot.db`; generate `AGENTS.md` only if missing | no |
| SQLite state | Reuse each workspace's `bot.db`; preserve channels, sessions, messages, tasks; use additive migrations only | no |
| Feishu receive contract | Keep per-app receiver shape and `IncomingMessage` model, including attachments and event/message IDs | real network yes |
| Feishu send contract | Keep `SendText`, `SendThinking`, `UpdateCard`; mock sender records calls | real network yes |
| Channel workers | One bounded serial worker per channel key, idle timeout, graceful shutdown, duplicate event handling | no |
| Session lifecycle | Active/archived sessions, `/new`, engine thread mapping, message persistence | no |
| Work mode | Reuse active engine thread, send thinking card, update final card | engine internals yes |
| Companion mode | Fresh engine thread every turn, direct text reply, memory/history continuity through workspace files | engine internals yes |
| Attachments | Attachment-only ack, pending attachment state, prompt merge, session attachment dir, cleanup | real Feishu download yes |
| Tasks | Watch/scan `tasks/`, parse YAML, mirror DB, schedule, run user/borrow/system tasks, `post_archive` | engine execution yes |
| Session context | Generate `SESSION_CONTEXT.md` and inject `<system_routing>` before each turn/task | no |
| Output shaping | Empty output guard, companion filter interface, `[[SEND]]` segmentation | no |
| Cleanup | Attachment cleanup schedule and retention config | no |
| Approval | Persist approval requests, state transitions, timeout, mock decisions | real Feishu approval card yes |
| Observability | Structured logs and lifecycle events with app/channel/session/thread/task/turn IDs | no |
| Debug API | Local-only simulation of receiver events, tasks, approvals, and mock engine scenarios | no |

## Runtime Architecture

```text
cmd/server
  -> config.Loader
  -> workspace.Initializer
  -> db.Registry
  -> store.Repositories
  -> feishu.Receiver(s) or debugapi.Receiver
  -> task.Watcher + task.Scheduler
  -> session.Manager
  -> http.Server for health/debug only

Receiver
  -> Dispatcher
  -> Channel Worker
  -> Session Service
  -> Turn Orchestrator
     -> sessionctx.Writer
     -> output.Filter / Segmenter
     -> guardrail.Pipeline
     -> engine.Engine
        -> mockengine.Engine
        -> codexapp.Engine in follow-up story
     -> approval.Broker
     -> feishu.Sender
  -> store.Repositories
```

The real Feishu receiver remains WebSocket-based. The HTTP server owns health
and debug endpoints only unless a future story explicitly adds webhook support.

`session.Manager` owns channel/session orchestration. It must not own app-server
process lifecycle, schema parsing, event normalization, approval state
transitions, or raw DB compatibility rules.

## Package Design

```text
cmd/server
  process entrypoint, lifecycle, health, debug routes

internal/config
  legacy config loading, redacted logging helpers, validation

internal/workspace
  idempotent workspace init, AGENTS bridge, skill warnings

internal/db
  SQLite open, additive migration, per-app registry

internal/store
  repository interfaces and GORM implementations for channels, sessions,
  messages, tasks, attachments, approvals, and turn metadata

internal/model
  persisted structs and constants; no orchestration logic

internal/feishu
  IncomingMessage, Attachment, Receiver, Sender contracts

internal/debugapi
  localhost-only debug receiver, deterministic scenario endpoints

internal/session
  channel workers, session lifecycle, /new, attachment merge orchestration

internal/sessionctx
  SESSION_CONTEXT.md writer and <system_routing> builder

internal/output
  empty-output guard, companion filter interface, [[SEND]] segmenter

internal/task
  YAML loader, watcher, scheduler, runner, cleanup

internal/engine
  neutral engine contract, turn events, approval requests, interrupt, usage

internal/mockengine
  deterministic app-server-like mock implementation

internal/codexapp
  app-server runtime manager, protocol client, event normalizer,
  schema fixture placement, approval/interrupt adapter interfaces

internal/approval
  approval request model, state machine, mock decisions, timeout handling

internal/guardrail
  body/output/event/duration/chat allowlist checks

internal/observability
  structured log field helpers and lifecycle event names
```

## Configuration Design

The new config keeps the legacy app shape and adds neutral engine/app-server
sections.

```yaml
server:
  port: 8080
  debug_enabled: true
  debug_bind: "127.0.0.1"
  max_body_bytes: 1048576

engine:
  type: mock # mock | codex-app-server

codex:
  app_server:
    listen: unix
    auth: capability-token
    approval_policy: untrusted
    schema_version: "0.142"
    runtime_dir: ./runtime/codex
    topology: per-app

session:
  worker_idle_timeout_minutes: 30
  queue_size: 64
  duplicate_message_ttl_hours: 24

attachments:
  pending_ttl_minutes: 30
  pending_max_items: 20
  max_bytes_per_attachment: 104857600

cleanup:
  attachments_retention_days: 7
  attachments_max_days: 30
  schedule: "0 2 * * *"

guardrails:
  max_message_bytes: 1048576
  max_output_bytes: 262144
  max_events_per_turn: 2000
  max_turn_duration_minutes: 90

approval:
  mock_policy: auto_allow # auto_allow | auto_deny | timeout
  timeout_seconds: 300

apps:
  - id: demo-assistant
    feishu_app_id: ""
    feishu_app_secret: ""
    feishu_verification_token: ""
    feishu_encrypt_key: ""
    workspace_dir: ./workspaces/demo-assistant
    workspace_mode: work
    allowed_chats: []
    claude:
      permission_mode: acceptEdits
      allowed_tools: []
      provider: anthropic
      model: sonnet
      effort: medium
```

Compatibility rules:

- Existing `claude` blocks are parsed but not treated as the runtime engine.
- Provider, model, effort, provider-specific config, and allowed tools are
  preserved for migration and future app-server config generation.
- Secrets are never printed in logs, test failures, debug responses, or copied
  into general Codex config.
- `server.debug_bind` must default to `127.0.0.1`; non-local binds require an
  explicit config value and must be covered by tests.

## Test Fixtures

Fixtures are part of the story deliverables and must be committed.

```text
testdata/
  legacy/
    config.redacted.yaml
    bot.db
    workspace_minimal/
      CLAUDE.md
      .claude/skills/
      .claude/story-state-SAMPLE.local.md
      memory/
      tasks/
      sessions/
    workspace_malformed_skill/
      .claude/skills/broken/SKILL.md
    tasks/
      user_reply.yaml
      borrow_channel.yaml
      borrow_channel_post_archive.yaml
      system.yaml
      disabled_no_cron.yaml
      invalid_missing_target.yaml
      invalid_mixed_target.yaml
```

Redaction rules:

- Preserve app count, app IDs shape, workspace modes, provider names, model
  fields, effort fields, allowed tool lists, task IDs, and DB schema.
- Replace Feishu app IDs, app secrets, verification tokens, encryption keys,
  provider auth tokens, and other secrets with stable placeholders.
- Redacted config must retain the observed legacy distribution: total apps,
  work/companion counts, and provider set including `anthropic` and `bailian`.

Legacy DB fixture requirements:

- At least one channel row.
- One active session with non-empty physical `claude_session_id`.
- One archived session.
- User and assistant messages linked to a session.
- One enabled task and one disabled task.
- Existing row counts and old columns must be asserted before and after
  migration.

## Workspace Compatibility

For each app:

1. Ensure workspace root exists.
2. Ensure `.codex/`, `.codex/skills/`, `.claude/skills/`, `memory/`, `tasks/`,
   `sessions/` exist.
3. Ensure `.memory.lock` exists.
4. Do not modify `CLAUDE.md`.
5. Do not overwrite an existing `AGENTS.md`.
6. If `AGENTS.md` is missing, generate only the bridge below with a managed
   header.
7. Preserve `.claude/story-state-*`.
8. Preserve `.claude/skills/feishu_ops/feishu.json` and enforce `0600` when it
   exists.
9. Detect malformed `SKILL.md` files and log a non-fatal warning with app ID,
   workspace path, and skill path.
10. Do not copy template files into existing workspaces unless the target file
    is framework-managed and missing.

Generated bridge:

```md
# Codex Workspace Bridge

This file is generated by codex_workspace_bot when AGENTS.md is missing.

Read CLAUDE.md as the legacy workspace instruction source, then follow
Codex-native rules from this file when they conflict.

Framework context for each turn is written to SESSION_CONTEXT.md.
```

App-server cwd must be the workspace root that contains `AGENTS.md`.

The scaffold uses local process state and per-workspace directories. It does
not introduce FC sessions, NAS mounts, or Dev/Use/Eval workspace states.

## App-Server Runtime Topology

The mock engine has no external runtime.

For the real app-server follow-up story, the default topology is one app-server
runtime per configured app:

```text
CODEX_HOME/state dir: {codex.app_server.runtime_dir}/{app_id}/
cwd:                  {app.workspace_dir}
socket/auth:          process-local under runtime dir
```

Rules:

- App-server cwd is the workspace root.
- Framework runtime files live under `codex.app_server.runtime_dir/{app_id}`.
- Runtime state is not stored in workspace `memory/`, `tasks/`, or `sessions/`.
- Auth tokens are process-local and never written to DB or logs.
- Tests must prove app A cannot derive or use app B runtime path from config.
- A different topology requires a new design decision before implementation.

## Database And Store Compatibility

The scaffold must continue to use each workspace's existing `bot.db`.

Rules:

- Additive migrations only.
- Never drop or rename existing tables or columns.
- Preserve `channels`, `sessions`, `messages`, and `tasks`.
- Keep physical `sessions.claude_session_id` readable and writable.
- Expose `sessions.claude_session_id` in Go as `EngineThreadID`.
- Use `TurnID` for each individual engine turn.
- Do not use `EngineSessionID` as a second name for app-server thread identity.
- New Codex-specific fields may be added only after fixture tests prove old DB
  files still open and old records remain readable.

Store boundary:

- Domain logic depends on repositories from `internal/store`, not raw GORM.
- DB migrations and old-column mappings live in `internal/db` and
  `internal/store`.
- Session, task, attachment, approval, and debug code must not duplicate SQL
  compatibility logic.

DB verification:

- Dump schema before and after migration in tests.
- Assert old tables and old columns still exist.
- Assert old row counts do not decrease.
- Assert old field values, including `claude_session_id`, remain readable.
- Assert `EngineThreadID` writes update the old physical column.

## Feishu Contracts

The scaffold keeps the original receiver/sender boundaries, while real network
calls are replaced by debug/mock implementations.

Receiver contract:

```text
IncomingMessage
  app_id
  chat_type              # p2p | group | topic_group or Feishu equivalent
  chat_id
  thread_id
  channel_key
  sender_id
  message_id
  prompt
  attachments[]
  receive_id
  receive_type
  received_at
```

Attachment contract:

```text
Attachment
  id
  kind                  # image | file
  original_name
  source_message_id
  temp_path
  session_path
  size_bytes
  created_at
```

Sender contract:

```text
SendText(ctx, receive_id, receive_type, text) -> message_id
SendThinking(ctx, receive_id, receive_type) -> card_message_id
UpdateCard(ctx, card_message_id, text)
```

Mock sender requirements:

- Record every call for automated tests.
- Preserve work-mode order: `SendThinking` before `UpdateCard`.
- Preserve companion direct-text behavior.
- Record failed sends without creating phantom delivered-message history.
- Never log secrets.

## Channel And Session Semantics

Channel key formulas must be derived from current `cc_workspace_bot` receiver
fixtures and tested for P2P, group root, group thread, and repeated thread
replies.

Baseline formulas:

- P2P: `p2p:{chat_id}:{app_id}`
- Group root: `group:{chat_id}:{app_id}`
- Thread: `thread:{chat_id}:{thread_id}:{app_id}`
- App ID is always the final segment.

Worker rules:

- One bounded queue per channel key.
- One active turn per worker.
- Worker exits after idle timeout.
- Duplicate `message_id` events are idempotent for
  `session.duplicate_message_ttl_hours`; persisted delivered message IDs survive
  restart.
- Queue overflow must create a deterministic rejection path: send a busy text
  response when possible, record a rejected event, and never silently drop.
- Context cancellation interrupts active turns and drains shutdown safely.

Session rules:

- Work mode reuses active session and `EngineThreadID`.
- Companion mode reuses workspace memory/history but starts a fresh engine
  thread for every user turn.
- `/new` clears pending attachments for the channel, archives active session,
  and clears engine continuity. The next turn must not reuse the old
  `EngineThreadID`.
- User messages are persisted when accepted for processing.
- Assistant messages are persisted only after successful final output or
  confirmed send, depending on sender semantics.
- Error, empty, interrupted, and approval-timeout paths record turn metadata
  without polluting assistant history with failed model output.

## Attachment Persistence State Machine

Attachment-only messages create channel-level pending attachment records and
send an acknowledgement. They may create a session if needed to move files into
the session directory; if no session exists, pending records must still be
durable across restart.

Pending attachment states:

```text
pending -> consumed
pending -> expired
pending -> cleared_by_new
pending -> deleted_by_cleanup
```

Rules:

- Pending records are persisted in SQLite and may reference temp filesystem
  files until consumed.
- Pending records expire after `attachments.pending_ttl_minutes`.
- Pending records are capped by `attachments.pending_max_items` per channel.
- `/new` moves pending records for that channel to `cleared_by_new`.
- The next non-attachment-only text message consumes pending records and merges
  references into the prompt in receive order.
- Consumed files are moved or copied into
  `sessions/{session_id}/attachments/`.
- Cleanup removes expired pending temp files and old session attachment dirs.
- Tests must cover restart between attachment-only and text events.

## SESSION_CONTEXT.md Golden Contract

Before every interactive, user-facing task, borrow-channel task, or system task
turn, the scaffold writes a framework-managed context file and overwrites it on
each run.

Interactive and channel task path:

```text
sessions/{session_id}/SESSION_CONTEXT.md
```

System task path:

```text
sessions/_system/{slug}/SESSION_CONTEXT.md
```

Required fields:

- app ID
- workspace mode
- workspace dir
- session ID
- channel key
- routing key
- chat type
- chat ID
- thread ID
- receive ID
- receive type
- sender ID
- message ID when present
- task ID and task name when present
- task target type and target ID when present
- attachments dir
- memory dir
- tasks dir
- `EngineThreadID` when present
- current timestamp

The prompt sent to the engine also includes `<system_routing>` metadata. Golden
tests must cover interactive, borrow-channel task, and system task context
files.

## Task Contract Compatibility

Task orchestration is not engine-specific and must be migrated in this story.

Task IDs:

- Canonical ID format is `{app_id}/{slug}`.
- The slug is derived from the YAML filename without extension.
- Legacy task IDs are migrated to canonical IDs.

Supported YAML fields:

```yaml
name: string
cron: string
enabled: bool
target_type: string
target_id: string
prompt: string
created_by: string
send_output: bool
post_archive: bool
```

Defaults:

- `enabled` defaults to true.
- `send_output` defaults to true.
- Disabled tasks may have an empty cron.
- Enabled tasks require a cron expression.

Target matrix:

| send_output | target_type | target_id | Mode | Valid |
|---|---|---|---|---|
| true | set | set | user-facing | yes |
| true | empty | any | invalid | no |
| true | any | empty | invalid | no |
| false | set | set | borrow-channel | yes |
| false | empty | empty | system | yes |
| false | set | empty | invalid | no |
| false | empty | set | invalid | no |

`post_archive=true` is valid only for borrow-channel tasks.

YAML/DB sync rules:

- YAML file content is source of truth when the file exists.
- Create/update YAML events upsert DB and scheduler state.
- Delete YAML events soft-delete or disable the DB task and remove scheduler
  state.
- Disabled YAML removes scheduler state but keeps DB metadata.
- Startup restores enabled DB tasks first, then rescans YAML to reconcile
  create/update/delete drift.
- Scheduler uses a fake clock in tests and prevents overlapping runs.

Execution routing:

- User-facing tasks enter the target channel worker through a synthetic
  message, preserving serialization with user messages.
- Borrow-channel tasks enter the target channel worker and may call
  `post_archive` only after successful engine execution.
- Borrow-channel failures, empty output, interrupted turns, or partial send
  failures do not archive the channel.
- System tasks run in an independent task worker under
  `sessions/_system/{slug}/` and do not create Channel or Session DB rows.
- Every task execution starts a fresh engine thread unless a later design
  explicitly changes that behavior.
- `last_run_at` is updated only after execution reaches a terminal state.

## Output Processing

Output handling is framework behavior and must not depend on app-server.

Rules:

- Aggregate streaming deltas into final assistant text.
- Preserve usage and duration metadata when supplied by engine.
- Treat empty final output as a handled failure with a user-facing message.
- Work mode updates the thinking card with final text.
- Companion mode sends direct text.
- Hook-dependent companion filtering is replaced by a Go-side filter interface.
- Filtering happens before segmentation.
- `[[SEND]]` segmentation happens after filtering.
- Leading/trailing markers are ignored.
- Empty segments are dropped.
- Stored assistant history replaces `[[SEND]]` markers with newlines.
- Segment sending uses deterministic test timing or injectable delay.

## Engine Boundary

The neutral engine package exposes app-server-shaped behavior without exposing
Codex protocol details to session/task code.

Minimum concepts:

```text
Engine
  EnsureThread(ctx, request) -> Thread
  SendTurn(ctx, request) -> EventStream
  RespondApproval(ctx, response)
  Interrupt(ctx, request)
  CloseThread(ctx, thread_id)

ThreadPolicy
  resume_existing
  force_new
  no_persist
```

Thread policy:

- Work mode uses `resume_existing`.
- Companion mode uses `force_new`.
- `/new` clears existing mapping before the next `resume_existing`.
- User-facing and borrow-channel tasks use `force_new`.
- System tasks use `force_new` with no channel routing.
- `CloseThread` means "forget framework mapping"; it must tolerate engines that
  do not expose a close primitive.

Event stream contract:

- `SendTurn` returns a bounded event stream plus a turn handle.
- `turn_started` is first when emitted.
- Zero or more delta/tool/approval events may follow.
- Exactly one terminal event must occur: `completed`, `failed`, or
  `interrupted`.
- No deltas are valid after a terminal event.
- Unknown event types fail tests unless explicitly allowed in schema fixtures.
- Malformed events become terminal `failed` events and are persisted as turn
  metadata.
- Context cancellation requests interruption and waits for terminal handling.
- `Interrupt` is the explicit user/orchestrator interrupt path.
- Event count is bounded by `guardrails.max_events_per_turn`.

Mock engine scenarios:

- `normal_delta`
- `completed_with_usage`
- `engine_error`
- `empty_output`
- `approval_requested`
- `approval_timeout`
- `interrupt_ack`
- `malformed_event`
- `slow_stream`

Debug scenario selection must be scoped per request or per test handle, never a
process-global mutable setting.

## App-Server Design

The real app-server implementation is a separate package and follow-up story.
This story creates only the package skeleton and interface contracts needed to
avoid later rewrites.

```text
internal/codexapp
  RuntimeManager
    owns process lifecycle, socket path, auth token, cwd, CODEX_HOME/state dir

  Client
    connects to stdio/unix/ws transport and sends protocol requests

  EventNormalizer
    maps app-server protocol events into internal/engine TurnEvent

  ApprovalAdapter
    maps app-server approval requests into approval.Request records

  InterruptAdapter
    sends turn interrupt/steer commands

  SchemaFixture
    placement convention for generated app-server schema tests
```

Design constraints:

- `approval_policy=untrusted` is required so approvals surface to the bot.
- App-server auth tokens are process-local and never logged.
- The package may borrow AIPM POC findings for protocol shape, streaming,
  approval, and interrupt behavior.
- The package must not borrow AIPM FC/NAS three-state sandbox topology.

Follow-up app-server story must independently verify:

- local Codex app-server schema generation
- app-server transport selected by config
- `CODEX_HOME` plus cwd `AGENTS.md` loading
- approval request emission under `approval_policy=untrusted`
- interrupt/steer behavior

## Approval State Machine

Approval is part of the first scaffold interface, even when user-facing Feishu
approval cards are not implemented yet.

Persisted model:

```text
approval_requests
  id
  app_id
  channel_key
  session_id
  turn_id
  engine_thread_id
  status
  request_json
  decision_json
  created_at
  resolved_at
  expires_at
```

States:

```text
requested -> auto_allowed
requested -> auto_denied
requested -> pending_user
pending_user -> user_allowed
pending_user -> user_denied
pending_user -> expired
requested -> interrupted
pending_user -> interrupted
```

Rules:

- Mock mode supports `auto_allow`, `auto_deny`, and `timeout`.
- Expired approvals must release or interrupt the active turn according to the
  mock scenario.
- Pending approvals are marked `expired` or `interrupted` on startup.
- Approval state changes are logged with app/channel/session/turn IDs.
- Worker shutdown must not hang on pending approval.

## Guardrails

Initial guardrails:

- message body size limit
- output size limit
- max event count per turn
- max turn duration
- app/chat allowlist
- debug body size limit
- no secret values in logs, debug responses, or test failures
- final output must be non-empty or a handled failure

Limit failures:

- create turn metadata with a deterministic failure reason
- send a user-facing error when possible
- do not call engine
- do not create assistant history entries

Boundary tests must cover exactly at limit, limit plus one, and missing config
default behavior.

## Debug API

Debug API exists to make the scaffold independently verifiable without Feishu.

Rules:

- Bind to `127.0.0.1` by default.
- Disabled unless `server.debug_enabled=true`.
- Enforce body size limit.
- Require configured `app_id`.
- Do not accept arbitrary workspace paths.
- Return stable JSON responses.
- Unknown app returns a deterministic error.
- Debug routes return 404 or 403 when disabled.

Endpoints:

```text
GET  /health
POST /debug/dispatch
POST /debug/task/run
POST /debug/approval/respond
POST /debug/engine/scenario
```

`/debug/dispatch` accepts text, optional mock attachments, and a per-request
mock engine scenario.

## Observability

All major events use structured log fields:

- `app_id`
- `channel_key`
- `session_id`
- `engine_thread_id`
- `message_id`
- `task_id`
- `turn_id`
- `event_type`
- `duration_ms`
- `input_tokens`
- `output_tokens`
- `error_kind`

Required lifecycle events:

- receiver message accepted
- dispatch queued
- dispatch rejected
- worker started/exited
- session created/reused/archived
- context written
- turn started
- delta received
- approval requested/resolved
- turn interrupted
- turn completed
- turn failed
- task scheduled/started/completed/failed
- attachment pending/consumed/expired/cleared

## Automated Verification

The implementation is accepted only if these automated checks pass.

Required command:

```bash
go test ./...
```

Required test groups:

1. Config tests
   - load checked-in template
   - load `testdata/legacy/config.redacted.yaml`
   - assert legacy app count, work/companion distribution, and provider set
   - preserve provider, model, effort, allowed tools, and app-level `claude`
     fields
   - no secret values appear in formatted logs/errors

2. Workspace init tests
   - create required dirs
   - idempotent on repeated runs
   - existing `CLAUDE.md` hash unchanged
   - existing `AGENTS.md` hash unchanged
   - missing `AGENTS.md` bridge generated with managed header
   - `.claude/skills`, `.claude/story-state-*`, `memory`, `tasks`, `sessions`,
     `bot.db` preserved
   - malformed `SKILL.md` warning includes app ID and workspace path
   - no template files copied into existing workspaces unless managed/missing

3. DB/store tests
   - open and migrate `testdata/legacy/bot.db`
   - old schema, tables, columns, row counts, and key field values preserved
   - `claude_session_id` maps to `EngineThreadID`
   - raw domain logic uses repository interfaces rather than direct GORM calls

4. Channel/session tests
   - old receiver fixtures produce expected channel keys
   - same channel serializes messages
   - different channels do not block each other
   - duplicate `message_id` is idempotent across restart within TTL
   - queue overflow sends busy/rejection behavior and records rejected event
   - `/new` archives active session, clears pending attachments, and next turn
     has a new `EngineThreadID`
   - work mode reuses engine thread
   - companion mode does not reuse engine thread
   - error/empty/approval/interrupt paths have expected DB state

5. Attachment tests
   - attachment-only ack
   - durable pending attachment state
   - pending expiry
   - `/new` isolation
   - restart between attachment-only and text event
   - next text prompt merges attachment references in order
   - files land in session attachments dir
   - cleanup removes expired pending files and old attachment dirs

6. Task tests
   - YAML load and app ID injection
   - default values
   - invalid target matrix rejects bad states
   - create/update/delete/disable YAML sync
   - scheduler restore after restart
   - scheduler prevents overlap using fake clock
   - user-facing task sends output through target channel worker
   - borrow-channel task suppresses output and supports successful
     `post_archive`
   - failed borrow-channel task does not archive
   - system task uses `sessions/_system/{slug}/` and creates no channel/session
     DB row
   - `last_run_at` updated at terminal state

7. Session context tests
   - golden file for interactive turn
   - golden file for borrow-channel task
   - golden file for system task
   - `<system_routing>` prompt metadata present

8. Output tests
   - no `[[SEND]]` marker
   - one marker
   - multiple markers
   - leading/trailing markers
   - empty segment removal
   - filter before segmentation
   - stored history strips markers

9. Engine contract tests
   - event ordering and one terminal event
   - completed usage captured
   - error path
   - empty output guard
   - approval request path
   - approval timeout path
   - interrupt path
   - malformed event path
   - max event count path
   - scenario selection scoped per request/test handle

10. Approval tests
    - request persisted
    - auto allow
    - auto deny
    - timeout
    - interrupt
    - startup expires or interrupts pending approvals
    - worker shutdown does not block

11. HTTP/debug tests
    - `/health` stable JSON
    - `/debug/dispatch` writes DB side effects
    - `/debug/task/run` triggers task runner
    - `/debug/approval/respond` updates approval state
    - debug API disabled behavior
    - unknown app
    - oversized body
    - workspace path injection rejected
    - no secret echo

12. Artifact tests
    - expected `.gitignore` patterns cover framework runtime artifacts:
      `runtime/`, `workspaces/`, `*.db`, `*.db-shm`, `*.db-wal`, `*.log`,
      `*.pid`
    - distinguish framework runtime artifacts from existing workspace state
    - `git status --ignored --short` after debug dispatch shows no unintended
      untracked runtime files

## Manual Verification

Manual checks must record concrete evidence: command output, SQLite query
results, mock sender call sequences, filesystem diffs, and warning lines.

1. Start from checked-in template and verify `/health` stable JSON.
2. Send work-mode debug text and verify mock call order:
   `SendThinking -> UpdateCard`.
3. Send two work-mode messages in one channel and query SQLite for one active
   session with reused `EngineThreadID`.
4. Send `/new`, then another message, and query SQLite for archived old session
   plus new `EngineThreadID`.
5. Send companion-mode messages and query turn records to verify fresh thread
   per turn.
6. Send attachment-only debug event, restart process, send text event, and
   verify prompt merge or deterministic expiry.
7. Run user-facing task and verify sender call plus `last_run_at`.
8. Run borrow-channel task with `post_archive=true` and verify archive only on
   success.
9. Run system task and verify `sessions/_system/{slug}/SESSION_CONTEXT.md`.
10. Run workspace init against copied legacy workspace and compare protected
    file hashes.
11. Start with malformed skill fixture and capture non-fatal warning.
12. Start with redacted legacy config and verify all apps initialize without
    secret logging.
13. Trigger queue overflow and verify busy/rejection response.
14. Trigger approval timeout scenario and verify worker remains usable.

Example SQLite checks should be included in README after implementation for:

- active vs archived sessions
- message persistence
- engine thread reuse or freshness
- task `last_run_at`
- approval status

## Implementation Milestones

The story is implemented in milestones. Each milestone must keep tests passing.

1. Config, fixtures, DB migration, store repositories.
2. Workspace initializer and protected-file idempotency tests.
3. Feishu contracts, debug receiver, mock sender, channel/session loop.
4. Session context writer, output filter/segmenter, mock engine contract.
5. Attachment pending state, merge, cleanup, restart behavior.
6. Task YAML loader, watcher, scheduler, runner, task routing.
7. Approval state machine, guardrails, interrupt handling.
8. Debug API hardening and manual evidence scripts.
9. Full legacy fixture verification and README runbook.

## Deliverables

This story produces:

- Go module and server entrypoint.
- Config template and committed legacy fixtures.
- Workspace initializer.
- SQLite models, additive migrations, and store repositories.
- Feishu receiver/sender contracts with debug/mock implementations.
- Session manager and channel workers.
- Durable attachment pending state and cleanup.
- Task watcher, scheduler, runner, and cleanup.
- Session context writer and routing injector.
- Output filter and segmenter.
- Neutral engine interface.
- Deterministic mock engine.
- App-server package skeleton with design-level interfaces only.
- Approval broker and state model.
- Guardrail pipeline.
- Debug API.
- Automated tests listed above.
- README runbook with run commands, verification commands, and manual evidence.

## Follow-Up Stories

1. Implement real Codex app-server runtime/client/event normalizer.
2. Implement Feishu WebSocket receiver and SDK sender.
3. Implement Feishu approval card UI.
4. Verify Bailian/provider-specific app-server configuration.
5. Add Langfuse or equivalent tracing integration.
