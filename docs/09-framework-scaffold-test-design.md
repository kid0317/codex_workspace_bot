# Framework Scaffold Test Design

Date: 2026-06-28

Target story:

- `docs/06-framework-scaffold-story-design.md`

## Test Goal

Prove that the first runnable `codex_workspace_bot` scaffold preserves
`cc_workspace_bot` non-engine behavior while the real Codex app-server and real
Feishu network calls are mocked.

Acceptance must not rely on a single happy-path debug message. The test suite
must prove:

- legacy config and workspace compatibility
- old SQLite DB compatibility
- session and channel behavior
- work and companion mode differences
- task YAML contract preservation
- attachment state lifecycle
- `SESSION_CONTEXT.md` and routing compatibility
- output filtering and segmentation
- mock engine event contract
- approval state machine
- guardrails and debug API safety
- old `cc_workspace_bot` non-engine regression parity
- manual verification evidence

## Test Scope

In scope:

- Unit tests for every non-engine domain package.
- Mock-backed integration tests through debug APIs and in-process services.
- Fixture-driven compatibility tests using redacted legacy config, DB, tasks,
  and workspaces.
- Manual verification using local debug endpoints and SQLite queries.

Out of scope for this story:

- Real Codex app-server protocol tests.
- Real Feishu WebSocket or SDK send tests.
- Feishu approval card UI tests.
- Provider-specific real runtime validation such as Bailian app-server config.

Those out-of-scope items belong to follow-up stories, but their interfaces must
be represented by mock contracts in this story.

## Risk Analysis

| Risk | Impact | Test response |
|---|---|---|
| Legacy config shape drifts | Existing 28 apps cannot start | Redacted legacy config fixture asserts app count, modes, providers, and legacy fields |
| Secrets leak in logs/errors | Security incident | Redaction tests and debug response tests search for placeholder secret values |
| Workspace init overwrites user files | Data loss | Hash `CLAUDE.md`, existing `AGENTS.md`, `.claude/story-state-*`, and secret-bearing files before/after init |
| Additive migration accidentally changes old DB | Lost history/tasks/session continuity | Schema and row-count diff against legacy DB fixture |
| `claude_session_id` mapping breaks | Session resume/thread continuity lost | Read/write `EngineThreadID` through old physical column |
| Channel key changes | Fragmented sessions or wrong `/new` behavior | Fixture events for P2P, group, group thread, repeated thread replies |
| Queue overflow silently drops messages | User data loss | Overflow test asserts busy response and rejected event record |
| Duplicate Feishu delivery reprocesses turn | Duplicate replies and DB rows | Duplicate `message_id` tests across process restart within TTL |
| Companion mode reuses thread | Personality/memory behavior diverges | Companion tests assert fresh `EngineThreadID` per turn |
| `/new` only archives session but leaves hidden state | Old context leaks into new turn | `/new` tests assert archived session, cleared pending attachments, fresh engine thread |
| Pending attachment lost or stale | User's later text references missing/wrong file | Durable pending attachment tests, expiry, `/new` isolation, restart flow |
| Task YAML parser changes contract | Existing tasks stop running | Task matrix tests for defaults, invalid states, create/update/delete/disable |
| Borrow-channel task races user message | Archive or output happens in wrong order | Task routing tests force target channel serialization |
| System tasks create chat state | DB pollution and confusing history | System task tests assert no Channel/Session row |
| Output filter/segmentation order wrong | Companion replies or history polluted | Output tests assert filter before segmentation and stored marker stripping |
| Mock engine hides app-server edge cases | Real app-server integration later rewrites core | Engine contract tests cover event order, terminal events, malformed events, approval, interrupt |
| Approval pending blocks worker | Dead channel after approval timeout/restart | Approval timeout/restart/shutdown tests |
| Debug API exposes unsafe surface | Local service can mutate arbitrary workspaces | Debug tests for disabled mode, unknown app, body limit, workspace path injection |
| Runtime artifacts pollute repo | Dirty workspace and accidental commits | Artifact tests check `.gitignore` and `git status --ignored --short` |
| Old Feishu parsing behavior regresses | Users lose rich text, thread replies, or welcome context | Receiver tests cover reply targets, rich post extraction, safe string, filename sanitize, welcome messages |
| Companion tone regresses | Companion bots sound operational or break immersion | Tone tests cover attachment ack, `/new` receipt, dirty-message filter corpus |
| Segment delivery fails mid-reply | Companion replies are truncated or duplicated | Segment tests cover continue-on-error, rate-limit retry, cancellation, marker stripping |
| Task watcher log spam or race | Malformed YAML floods logs or races under file churn | Parse-error cache tests cover dedupe, hash breakthrough, cap, prune, forget, concurrent access |
| Task ID migration edge case breaks old tasks | Legacy tasks are duplicated, lost, or collide | Migration tests cover bare ID, UUID filename, dotted prefix, conflicts, empty app ID, idempotency, multi-workspace |
| Cleanup deletes another app's files | Cross-app data loss | Cleanup tests cover wrong-app isolation and retention cutoffs |
| Observability fail-open behavior regresses | One bad turn breaks telemetry or later turns | Telemetry tests cover stable IDs, zero usage, malformed rows, state atomicity, fail-open isolation |
| Store boundary erodes | Session/task code couples to SQLite details | Static import test rejects direct GORM usage outside DB/store packages |
| Shutdown hangs | Process cannot stop during slow stream, pending approval, or segment send | Shutdown integration tests cover active stream, approval, task, scheduler, segment send |

## Test Fixtures

Required fixture tree:

```text
testdata/
  legacy/
    config.redacted.yaml
    bot.db
    events/
      p2p_text.json
      group_text.json
      group_thread_text.json
      duplicate_message.json
      post_message.json
      bot_added_to_group.json
      user_added_to_group.json
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
  output_filter/
    dirty_messages.yaml
    send_marker_cases.yaml
    gate_cases.yaml
  telemetry/
    langfuse_dryrun_rows.jsonl
    malformed_rows.jsonl
  chat_history/
    bot.db
    SESSION_CONTEXT.md
    expected_outputs.yaml
```

Fixture rules:

- Redacted config keeps shape and counts, replacing secrets with stable tokens.
- DB fixture keeps legacy schema and representative rows.
- Workspace fixtures are copied into temporary directories for tests.
- Event fixtures represent normalized Feishu event inputs, not live credentials.
- Output filter fixtures preserve old companion dirty-message and `[[SEND]]`
  marker regression cases.
- Telemetry fixtures preserve old fail-open, stable ID, zero usage, and
  malformed-row behavior without requiring the old hook implementation.
- Chat history fixtures prove generated `SESSION_CONTEXT.md` and SQLite shape
  remain consumable by existing workspace skills.
- Tests may generate additional temporary DBs, but compatibility assertions must
  include the committed legacy DB fixture.

## Old Test Parity Matrix

The scaffold tests must explicitly classify old non-engine tests as one of:

- `ported`: same behavior and similar test structure.
- `translated`: old engine/hook detail replaced by an equivalent Go framework
  contract.
- `retired`: intentionally removed because it only tested Claude-specific
  engine behavior.

Required parity coverage:

| Old test asset | New coverage requirement |
|---|---|
| `internal/config/config_test.go` | config defaults, validation, allowed chats, provider/model fields, file-not-found |
| `internal/db/db_test.go` | DB open/migrate/idempotent/invalid path |
| `internal/workspace/init_test.go` | dirs, `.memory.lock`, idempotency, no overwrite, symlink skip if template copy exists |
| `internal/feishu/receiver_test.go` | channel key, reply target, post text extraction, safe string, filename sanitize, welcome message |
| `internal/session/manager_test.go` | archive active only, idempotent archive, no-active no-op |
| `internal/session/segment_test.go` | split segments, extra marker cases, typing delay |
| `internal/session/filter_test.go` | filtered reply consumption, canary, fail-open filter, companion tone |
| `internal/session/worker_test.go` | segment error/rate-limit/cancel, work card fallback, marker stripping, attachment path relocation |
| `internal/task/runner_test.go` | task channel key, receive target, YAML matrix, unresolved placeholders, post archive, system slug |
| `internal/task/scheduler_test.go` | add/remove/replace/invalid cron/remove non-existent |
| `internal/task/watcher_test.go` | parse-error cache dedupe, hash breakthrough, cap, prune, forget, concurrency |
| `internal/task/migration_test.go` | bare ID, UUID filename, dotted prefix, conflict, empty app ID, idempotent, multi-workspace |
| `internal/task/cleanup_test.go` | retention cutoffs, multiple apps, wrong-app isolation |
| `tests/output_filter/*` | dirty-message corpus, gate/fail-open behavior, snapshot precedence, marker invariants |
| `scripts/langfuse_dryrun/tests/*` | telemetry stable IDs, usage normalization, zero preservation, fail-open, atomic state |
| `chat_history` skill tests | generated context + DB compatible with channel-scoped history search |

## Unit Test Design

### `internal/config`

Goals:

- Load checked-in template.
- Load `testdata/legacy/config.redacted.yaml`.
- Validate app count, work/companion distribution, providers, provider fields,
  app-level `claude` fields, session defaults, cleanup defaults, guardrail
  defaults.
- Reject invalid app IDs, missing workspace dirs, invalid engine type, unsafe
  debug bind rules when configured.
- Reject malformed config fixtures while keeping file-not-found and invalid-path
  errors deterministic.
- Redact all secret-like fields in logs and errors.

Key assertions:

- Legacy `claude` fields are preserved but do not select runtime engine.
- `engine.type=mock` is accepted for scaffold.
- `codex.app_server.topology=per-app` resolves runtime paths under app ID.
- Runtime dirs for different apps are siblings under `runtime_dir` and never
  derived from `workspace_dir`.
- App A cannot override, resolve, or use app B runtime dir, socket path, or
  auth token.

### `internal/workspace`

Goals:

- Create required directories.
- Generate `AGENTS.md` only when missing.
- Preserve existing `CLAUDE.md`, existing `AGENTS.md`, `.claude/story-state-*`,
  `.claude/skills`, `memory`, `tasks`, `sessions`, and `bot.db`.
- Preserve `.memory.lock` and never overwrite its existing content.
- Preserve `feishu_ops/feishu.json` and enforce `0600`.
- Detect malformed skills and emit structured non-fatal warnings.

Key assertions:

- Protected file hashes are identical before and after init.
- Repeated init is idempotent.
- Generated bridge content matches the managed bridge contract.
- No template file is copied into existing workspace unless framework-managed
  and missing.
- If template copying remains in the implementation, symlinks are skipped and
  existing target files are not overwritten.

### `internal/db` and `internal/store`

Goals:

- Open and migrate the legacy DB fixture.
- Return deterministic errors for invalid DB paths.
- Preserve old schema, old columns, old row counts, old key values.
- Map physical `sessions.claude_session_id` to Go `EngineThreadID`.
- Keep domain packages using repositories instead of direct GORM calls.

Key assertions:

- Schema diff contains no dropped/renamed old table or column.
- Updating `EngineThreadID` changes `sessions.claude_session_id`.
- Store methods cover channel, session, message, task, attachment, approval,
  and turn metadata operations.
- Static architecture test fails if domain packages import `gorm.io/gorm`
  directly outside `internal/db`, `internal/store`, or test helpers.

### `internal/feishu`

Goals:

- Normalize receiver inputs into `IncomingMessage`.
- Build channel keys for P2P, group root, and group thread fixtures.
- Compute reply target: P2P replies use sender open ID; group and topic replies
  use chat ID.
- Extract rich post text, including title and multi-line text, while ignoring
  non-text tags and falling back on invalid JSON.
- Sanitize filenames against path traversal, slashes, backslashes, empty names,
  and unsafe characters.
- Generate welcome messages for bot-added and user-added events with app ID,
  group/member fallback names, and `/new` guidance.
- Preserve `message_id`, `receive_id`, `receive_type`, sender, thread, and
  attachment metadata.
- Mock sender records calls and failures deterministically.

Key assertions:

- Work-mode sender order is `SendThinking`, then `UpdateCard`.
- Companion mode uses `SendText`.
- Failed `SendText` does not create delivered-message history.
- Secret fields are absent from sender logs.
- Nil or missing Feishu fields are handled safely.

### `internal/session`

Goals:

- Ensure one worker per channel and ordered processing.
- Prove different channels do not block each other.
- Handle duplicate messages, queue overflow, `/new`, companion fresh thread,
  work thread reuse, and engine failure paths.

Key assertions:

- Same channel messages preserve input order.
- Duplicate message ID creates no duplicate assistant output.
- Duplicate survives restart within configured TTL.
- Queue overflow records rejected event and sends busy response when possible.
- `/new` archives session, clears pending attachments, and resets
  `EngineThreadID`.
- Error, empty, approval timeout, and interrupt paths write turn metadata but
  do not pollute assistant history.
- Archive service archives only the active target session, is idempotent, and
  no-ops cleanly when no active session exists.
- Companion segment sending continues after non-rate-limit segment send errors.
- Feishu rate-limit error code `99991400` retries once and respects context
  cancellation during delay/backoff.
- Work-mode thinking-card failure falls back to exactly one plain text send and
  does not apply companion segmentation.

### `internal/sessionctx`

Goals:

- Generate `SESSION_CONTEXT.md` for interactive, borrow-channel task, and
  system task paths.
- Inject `<system_routing>` into prompts.

Key assertions:

- Golden files match expected fields.
- Context file is overwritten per turn.
- System task path uses `sessions/_system/{slug}/SESSION_CONTEXT.md`.
- Interactive context includes chat/thread/receive/message fields.
- Generated context supports existing chat-history skill behavior: current
  channel filtering, role filtering, keyword filtering, day range, limit,
  truncation, missing/empty channel-key errors, and output including channel key
  and session ID.

### `internal/output`

Goals:

- Apply companion filter before segmentation.
- Segment on `[[SEND]]`.
- Strip markers from stored history.
- Handle empty output.
- Preserve old companion dirty-message corpus behavior.
- Preserve filter canary and fail-open behavior.

Key assertions:

- No marker, one marker, multiple marker, leading/trailing marker, and empty
  segment cases pass.
- Filter returns raw text on absent filter result.
- Empty final output produces handled failure.
- Segment delay is injectable or deterministic in tests.
- Operational sentences are removed while in-character sentences are retained.
- `[[SEND]]` marker count never increases; leading and adjacent markers collapse
  safely.
- Filter gates cover recursion guard, task-run skip, missing channel key,
  init-status skip, turn-start snapshot precedence, timeout, failure, and
  internal crash fail-open.
- Canary arms after first good filtered reply, warns on threshold misses, and
  resets on recovery.

### `internal/task`

Goals:

- Preserve task YAML contract.
- Validate target matrix and defaults.
- Mirror YAML to DB and scheduler.
- Restore state on startup.
- Route user-facing and borrow-channel tasks through target channel worker.
- Run system tasks independently.
- Preserve watcher parse-error cache behavior under high churn and concurrency.
- Preserve task ID migration edge cases from legacy DBs.
- Preserve cleanup isolation across apps.

Key assertions:

- `send_output` defaults true.
- `enabled` defaults true.
- Disabled tasks may omit cron.
- Delete/disable removes scheduler state.
- User-facing task sends output through target channel worker.
- Borrow-channel task suppresses output and archives only after success when
  `post_archive=true`.
- Failed borrow-channel task does not archive.
- System task creates no Channel or Session DB row.
- Scheduler uses fake clock and prevents overlapping runs.
- `last_run_at` updates only at terminal state.
- Unresolved `__PLACEHOLDER__` values are rejected before scheduling.
- Task ID migration covers empty DB, already migrated rows, bare names, UUID
  filenames, legacy dotted prefixes, conflict drop, empty app ID, idempotency,
  and multiple workspaces.
- Watcher parse-error cache covers dedupe by path/hash/error, hash
  breakthrough, capped cache, forget-on-success, prune-missing-paths, and
  concurrent access.
- Scheduler add/replace/remove/remove-nonexistent and invalid cron behavior is
  explicit.
- Cleanup covers archived retention, active max-days behavior, multiple apps,
  and wrong-app isolation.

### `internal/engine` and `internal/mockengine`

Goals:

- Enforce the neutral event stream contract.
- Simulate app-server-like scenarios deterministically.

Key assertions:

- `turn_started` is first when emitted.
- Exactly one terminal event is produced.
- No deltas after terminal event.
- Unknown events fail unless allowed by schema fixture.
- Malformed event becomes terminal failed event.
- Context cancellation requests interruption.
- `ThreadPolicy` behavior matches work, companion, task, system task, and
  `/new` semantics.
- Scenario selection is per request/test handle, not global mutable state.
- Invalid event sequences are table-tested: delta before required start,
  approval after terminal, completed then failed, interrupted then delta, and
  stream close without terminal.

### `internal/approval`

Goals:

- Persist approval requests and state transitions.
- Support mock auto allow, auto deny, timeout, interrupt, startup recovery.

Key assertions:

- Request records include app, channel, session, turn, thread, timestamps.
- Valid transitions only.
- Pending approvals expire or interrupt on startup.
- Worker shutdown is not blocked by pending approvals.
- Approval logs include stable identifiers and no secrets.
- Pending approval during process shutdown does not block shutdown and produces
  deterministic final state.

### `internal/guardrail`

Goals:

- Enforce body/output/event/duration/chat limits before engine invocation when
  possible.

Key assertions:

- At-limit passes.
- Limit plus one fails.
- Missing config applies defaults.
- Limit failure records turn metadata, sends user-facing error when possible,
  does not call engine, and does not create assistant history.
- Chat allowlist denies produce deterministic user-visible/no-op behavior
  according to receiver contract and never call engine.

### `internal/debugapi`

Goals:

- Provide safe local verification without Feishu or app-server.

Key assertions:

- `/health` stable JSON.
- Debug disabled returns 404 or 403.
- Unknown app returns deterministic error.
- Oversized body rejected.
- Workspace path injection rejected.
- No secret echo.
- `/debug/dispatch` creates DB side effects.
- `/debug/task/run` triggers task runner.
- `/debug/approval/respond` updates approval state.

### `internal/observability`

Goals:

- Preserve stable telemetry/log contracts without depending on old hooks.
- Fail open when telemetry inputs are missing or malformed.

Key assertions:

- Lifecycle events use stable event names and JSON fields.
- Usage normalization preserves explicit zero values.
- Multi-row assistant content merges without losing turn boundaries.
- Trace/turn/observation IDs are stable and namespace-separated.
- Telemetry state or offset writes are atomic under concurrent updates.
- Missing input/meta, malformed rows, sidechain/subagent rows, and synthetic
  rows do not crash the turn path or leak tool internals.
- One turn's telemetry error does not suppress later turn telemetry.

### Architecture Boundary Tests

Goals:

- Prevent accidental coupling to concrete persistence or global mock state.

Key assertions:

- Direct GORM imports are forbidden outside DB/store packages and test helpers.
- Mock engine scenarios are scoped per request or per test handle.
- Runtime path resolution is per app and cannot be overridden by debug payloads.

### Race And Shutdown Tests

Goals:

- Prove the scaffold stops cleanly and remains race-free under critical
  concurrency paths.

Key assertions:

- `go test -race ./...` passes for session, task watcher, scheduler, approval,
  and attachment packages.
- Slow stream is interrupted by process shutdown.
- Pending approval does not block shutdown.
- Segment sending exits on context cancellation.
- Task scheduler stops without starting new jobs after shutdown begins.
- Pending task reaches a deterministic terminal/recovery state on restart.

## Mock-Backed Integration Test Design

All integration tests run locally with:

- redacted config fixture
- temp copied workspace fixtures
- temp copied DB fixture
- mock Feishu receiver/sender
- mock engine
- fake clock for scheduler/expiry where needed
- in-process HTTP server or `httptest`

### INT-01: Work-Mode Happy Path

Flow:

1. Start app with mock engine scenario `normal_delta`.
2. Dispatch work-mode text message.
3. Worker creates active session.
4. Mock sender records `SendThinking`.
5. Mock engine emits deltas and terminal completed.
6. Mock sender records `UpdateCard`.

Assertions:

- DB has user message, assistant message, active session, `EngineThreadID`.
- Turn metadata has completed status and usage.
- Sender call order is correct.
- Logs include app/channel/session/turn IDs.

### INT-02: Work-Mode Thread Reuse

Flow:

1. Dispatch two messages to same channel.
2. Mock engine returns a stable thread on first message.

Assertions:

- Same active session is reused.
- Same `EngineThreadID` is reused.
- Message order is preserved.

### INT-03: `/new` Session Reset

Flow:

1. Dispatch normal work-mode message.
2. Dispatch attachment-only message.
3. Dispatch `/new`.
4. Dispatch normal message.

Assertions:

- Old session archived.
- Pending attachment state is `cleared_by_new`.
- New active session created.
- New `EngineThreadID` differs from old one.

### INT-04: Companion Fresh Thread

Flow:

1. Dispatch two companion-mode messages to same channel.

Assertions:

- Direct text sends, no thinking card.
- Same workspace memory/history dirs exist.
- Engine thread differs per turn.
- Stored assistant history strips `[[SEND]]` markers.

### INT-05: Attachment Restart Flow

Flow:

1. Dispatch attachment-only event.
2. Stop app cleanly.
3. Restart with same DB/temp workspace.
4. Dispatch text message.

Assertions:

- Pending attachment survives restart if TTL valid.
- Prompt includes attachment references in receive order.
- Files land in session attachments dir.
- Pending state becomes consumed.

### INT-06: Attachment Expiry

Flow:

1. Dispatch attachment-only event.
2. Advance fake clock past pending TTL.
3. Run cleanup.
4. Dispatch text message.

Assertions:

- Pending record becomes expired or deleted_by_cleanup.
- Expired attachment is not merged into prompt.
- Temp file removed if present.

### INT-07: User-Facing Task

Flow:

1. Load `user_reply.yaml`.
2. Trigger task through `/debug/task/run`.

Assertions:

- Task enters target channel worker.
- Engine uses fresh thread.
- Sender sends task output.
- Message recorded only after successful send.
- `last_run_at` updated.

### INT-08: Borrow-Channel Task With Post Archive

Flow:

1. Create active target channel session.
2. Run `borrow_channel_post_archive.yaml`.

Assertions:

- Task enters target channel worker.
- No direct sender output.
- Active session archived after successful terminal state.
- Failure scenario does not archive.

### INT-09: System Task

Flow:

1. Run `system.yaml`.

Assertions:

- Session dir is `sessions/_system/{slug}/`.
- `SESSION_CONTEXT.md` exists there.
- No Channel or Session DB row created.
- Any model text is discarded.
- `last_run_at` updated.

### INT-10: Approval Timeout

Flow:

1. Dispatch message with mock engine `approval_timeout`.
2. Let fake clock pass approval timeout.

Assertions:

- Approval request persisted.
- Status transitions to expired.
- Turn reaches terminal failed/interrupted state.
- Worker accepts next message.

### INT-11: Queue Overflow

Flow:

1. Configure small queue.
2. Block worker with slow stream.
3. Dispatch messages beyond capacity.

Assertions:

- Overflow messages create rejected event records.
- Busy response attempted when possible.
- Process does not panic.
- Already queued messages complete in order.

### INT-12: Debug API Safety

Flow:

1. Start with debug disabled.
2. Call debug routes.
3. Start with debug enabled.
4. Call unknown app, oversized body, workspace path injection payload.

Assertions:

- Disabled routes return 404 or 403.
- Unknown app deterministic error.
- Oversized body rejected.
- Workspace path injection rejected.
- No response contains secret placeholders.

### INT-13: Graceful Shutdown With Active Work

Flow:

1. Start slow-stream turn, pending approval, scheduled task, and companion
   segment send in controlled test instances.
2. Trigger process context cancellation.

Assertions:

- All workers exit.
- Active turn is interrupted or marked terminal.
- Pending approval is expired or interrupted.
- Scheduler stops and starts no new jobs.
- No goroutine leaks are detected by test timeout/race run.

### INT-14: Observability Fail-Open

Flow:

1. Run turns with normal usage, explicit zero usage, malformed telemetry rows,
   missing metadata, and a forced telemetry write failure.

Assertions:

- User-facing turn still completes when telemetry fails.
- Stable event fields are emitted for valid turns.
- Zero usage values are preserved.
- Later turns still emit telemetry after one failed turn.

### INT-15: Chat History Compatibility

Flow:

1. Generate `SESSION_CONTEXT.md` and DB rows from fixture.
2. Run the existing chat-history skill compatibility fixture or equivalent
   local test wrapper.

Assertions:

- Current channel is read from generated context.
- Query is scoped to current channel.
- Role, keyword, days, limit, and truncation filters work.
- Missing or empty channel key produces deterministic error.

## Manual Verification Test Cases

Manual verification runs after automated tests pass. Each case must produce
evidence that can be attached to the story delivery notes.

### MAN-00: 28-App Compatibility Smoke

Steps:

1. Load redacted legacy config.
2. Initialize copied workspaces for every app.
3. Start scaffold with mock engine.
4. Capture app readiness summary.

Expected:

- App total, work/companion counts, and provider set match fixture.
- Every app reaches ready state.
- No secret appears in logs.

Evidence:

- Startup summary.
- Redacted log excerpt.
- Workspace init report.

### MAN-01: Health And Startup

Steps:

1. Start server with checked-in template.
2. Call `/health`.
3. Inspect startup logs.

Expected:

- Stable JSON health response.
- Logs include app IDs and runtime mode.
- Logs do not include secrets.

Evidence:

- Command output.
- Redacted startup log excerpt.

### MAN-02: Work-Mode Message

Steps:

1. Dispatch work-mode debug message.
2. Inspect mock sender calls.
3. Query SQLite messages and sessions.

Expected:

- `SendThinking -> UpdateCard`.
- Active session exists.
- User and assistant message exist.
- `EngineThreadID` non-empty.

Evidence:

- Debug response.
- Mock sender call log.
- SQLite query output.

### MAN-03: Thread Reuse And `/new`

Steps:

1. Send two work messages.
2. Query `EngineThreadID`.
3. Send `/new`.
4. Send another message.
5. Query sessions again.

Expected:

- First two turns reuse thread.
- `/new` archives old active session.
- Post-`/new` turn has a different thread.

Evidence:

- SQLite query output for sessions and turn metadata.

### MAN-04: Companion Mode

Steps:

1. Dispatch two companion messages.
2. Inspect sender calls and turn records.

Expected:

- Direct `SendText`.
- No thinking card.
- Fresh `EngineThreadID` per turn.
- `[[SEND]]` segmented output sends multiple text messages when scenario uses
  markers.
- Attachment ack and `/new` receipt use companion tone and avoid operational
  wording.

Evidence:

- Mock sender calls.
- SQLite turn records.

### MAN-05: Attachment Pending And Restart

Steps:

1. Dispatch attachment-only debug event.
2. Restart process.
3. Dispatch text event.
4. Query attachment records and session dir.

Expected:

- Pending survives or expires deterministically according to TTL.
- If valid, attachment references merge into prompt.
- Files appear under session attachments dir.

Evidence:

- SQLite attachment query.
- Filesystem listing.
- Recorded prompt or turn metadata.

### MAN-06: Task Modes

Steps:

1. Run user-facing task.
2. Run borrow-channel task with `post_archive=true`.
3. Run system task.

Expected:

- User-facing sends output and updates `last_run_at`.
- Borrow-channel suppresses direct output and archives after success.
- System task writes under `sessions/_system/{slug}/` and creates no channel
  session row.

Evidence:

- Mock sender log.
- SQLite task/session query.
- Filesystem listing for system task context.

### MAN-07: Workspace Init Compatibility

Steps:

1. Copy legacy workspace fixture.
2. Record hashes for protected files.
3. Run workspace init twice.
4. Diff hashes and list generated files.

Expected:

- Protected hashes unchanged.
- Missing `AGENTS.md` generated only once.
- `.claude/story-state-*` remains.
- Malformed skill warning is non-fatal when using malformed fixture.

Evidence:

- Hash output before/after.
- Warning log line.
- File listing.

### MAN-08: Approval Timeout Recovery

Steps:

1. Dispatch debug message with approval timeout scenario.
2. Wait or advance fake/manual timeout.
3. Dispatch a normal message to same channel.

Expected:

- Approval status becomes expired.
- Timed-out turn is terminal.
- Worker handles next message.

Evidence:

- Approval table query.
- Turn table query.
- Second message response.

### MAN-09: Guardrail Failure

Steps:

1. Send message over configured body limit.
2. Send message at exact limit if practical.

Expected:

- Over-limit request fails before engine.
- Deterministic user-facing error when possible.
- At-limit request accepted.
- No assistant history for rejected request.

Evidence:

- Debug response.
- Mock engine call count.
- SQLite turn/message query.

### MAN-10: Artifact Cleanliness

Steps:

1. Run a debug dispatch.
2. Run `git status --ignored --short`.

Expected:

- Runtime files match ignored patterns.
- No unintended untracked source files.

Evidence:

- `git status --ignored --short` output.

### MAN-11: Feishu Rich Text And Topic Thread

Steps:

1. Dispatch rich post debug fixture.
2. Dispatch topic/group thread debug fixture.
3. Inspect normalized message and channel key.

Expected:

- Rich post title/text is preserved.
- Unsupported non-text tags do not pollute prompt.
- Thread channel key is stable across repeated replies.

Evidence:

- Debug normalized message output.
- SQLite channel/session query.

### MAN-12: Delivery Failure And Rate Limit

Steps:

1. Run companion segmentation scenario with one segment send failure.
2. Run rate-limit scenario with code `99991400`.
3. Run work-mode card failure scenario.

Expected:

- Companion continues after non-rate-limit segment failure.
- Rate-limit retries once.
- Work-mode card failure sends exactly one plain text fallback.

Evidence:

- Mock sender call log.
- Turn status query.

## Acceptance Gate

The story can be accepted only when:

- `go test ./...` passes.
- All required fixture files exist and are used by tests.
- Unit tests cover every domain listed in this document.
- Mock-backed integration tests cover all critical flows.
- Manual verification cases have recorded evidence.
- Old test parity matrix items are classified as `ported`, `translated`, or
  `retired`, with P0 parity items implemented.
- `go test -race ./...` passes for concurrency-sensitive packages or an
  explicitly documented package subset covering session/task/approval/attachment
  paths.
- No test or log output contains redacted secret placeholder values.
- The real app-server and real Feishu network remain replaceable behind their
  defined contracts.

## Traceability Matrix

| Story area | Unit tests | Integration tests | Manual tests |
|---|---|---|---|
| Config compatibility | `internal/config` | INT-12 | MAN-01 |
| Workspace init | `internal/workspace` | startup fixture flow | MAN-07 |
| DB/store compatibility | `internal/db`, `internal/store` | INT-01, INT-03 | MAN-02, MAN-03 |
| Feishu contracts | `internal/feishu` | INT-01, INT-04 | MAN-02, MAN-04 |
| Channel/session | `internal/session` | INT-01, INT-02, INT-03, INT-11 | MAN-02, MAN-03 |
| Attachments | `internal/session`, store | INT-05, INT-06 | MAN-05 |
| Tasks | `internal/task` | INT-07, INT-08, INT-09 | MAN-06 |
| Session context | `internal/sessionctx` | INT-09 | MAN-06, MAN-07 |
| Output processing | `internal/output` | INT-04 | MAN-04 |
| Engine contract | `internal/engine`, `internal/mockengine` | all turn flows | MAN-02, MAN-08 |
| Approval | `internal/approval` | INT-10 | MAN-08 |
| Guardrails | `internal/guardrail` | INT-12 | MAN-09 |
| Debug API | `internal/debugapi` | INT-12 | MAN-01, MAN-10 |
| Old Feishu parsing | `internal/feishu` | INT-12 | MAN-11 |
| Companion tone/delivery | `internal/output`, `internal/session` | INT-04 | MAN-04, MAN-12 |
| Observability | `internal/observability` | INT-14 | MAN-02, MAN-08 |
| Chat history compatibility | `internal/sessionctx`, skill fixture | INT-15 | MAN-06 |
| Race/shutdown | session/task/approval/attachment packages | INT-13 | MAN-08, MAN-12 |
| Artifact cleanliness | artifact test | debug dispatch artifact flow | MAN-10 |
