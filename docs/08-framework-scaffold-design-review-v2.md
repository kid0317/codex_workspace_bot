# Framework Scaffold Design Review v2

Date: 2026-06-28

Reviewed document:

- `docs/06-framework-scaffold-story-design.md`

Reference documents:

- `docs/00-cc-workspace-bot-research.md`
- `docs/02-workspace-compatibility-plan.md`
- `docs/07-framework-scaffold-design-review.md`

## Executive Summary

`06-framework-scaffold-story-design.md` v2 fixes the major gaps from the first
review. It now treats task orchestration, attachments, session context, DB
compatibility, companion mode, app-server separation, approval/interrupt, debug
API safety, observability, and automated verification as first-class design
requirements.

The remaining issues are no longer about missing whole subsystems. They are
mostly about precision and implementation boundaries:

1. The scope is now large enough that the implementation must be phased inside
   the story, otherwise the first runnable milestone can become too broad.
2. Several compatibility contracts are named but still need exact data
   schemas, state transitions, and failure semantics.
3. App-server is well separated as a future implementation package, but the
   mock/scaffold still needs a stronger contract to prevent `session.Manager`
   and task runner from depending on mock-only behavior.
4. The automated verification matrix is strong, but some tests need fixtures
   and deterministic inspection points defined up front.

Recommendation: v2 is good enough to become the implementation design after one
small tightening pass. Do not remove the expanded scope, but add explicit
sub-milestones and concrete contracts for tasks, attachments, session context,
approval records, and legacy DB fixtures.

## Review 1: CC Workspace Bot Technical Lead

### 1. Overall Judgment

From the original `cc_workspace_bot` behavior perspective, v2 is a substantial
improvement. It now directly covers the main reusable layers identified in the
research document: Feishu receive/send contracts, app routing, per-channel
workers, SQLite state, task scheduling, attachment handling, and workspace
initialization.

The design also correctly removes old engine-specific behavior from the generic
layer. It preserves the original orchestration model while allowing the real
engine to be mocked. This matches the migration direction in `00`, where the
Claude-specific layer is isolated and the reusable bot layers remain.

The remaining compatibility risk is that several old behaviors are described at
the outcome level but not yet at the old-system contract level. If implemented
too freely, the new bot could pass v2 tests while still differing from existing
workspace expectations.

### 2. Fixed Since The Previous Review

- Task subsystem is now in scope. v2 requires watching `tasks/`, parsing YAML,
  mirroring DB records, scheduling cron tasks, supporting user-facing,
  borrow-channel, system tasks, and `post_archive`.
- Companion semantics are now explicit: work mode reuses engine thread,
  companion mode starts a fresh engine thread every user turn while preserving
  memory/history continuity.
- Attachment behavior is now in the first scaffold data model, including
  attachment-only acknowledgement, pending cache, prompt merge, and session
  attachment directories.
- `SESSION_CONTEXT.md` and `<system_routing>` are now explicitly required before
  every interactive or task turn.
- Legacy DB compatibility is now explicit, including reuse of each workspace's
  `bot.db`, additive migrations, and the physical `sessions.claude_session_id`
  mapping.
- App-server is separated into `internal/codexapp` with runtime, client, event
  normalizer, approval, interrupt, and schema fixture responsibilities.
- Feishu sender contract now includes `SendText`, `SendThinking`, and
  `UpdateCard`.
- Debug API safety and automated verification are now much stronger than v1.

### 3. Remaining High-Priority Issues

#### 3.1 Task YAML contract is preserved, but not specified

v2 says not to redesign the task YAML contract and lists required task modes.
That is correct, but it does not point to the exact legacy fields, validation
matrix, default values, or DB mirror behavior.

Risk: the new implementation may create a compatible-looking task subsystem
that accepts a different field matrix or writes subtly different task IDs. That
would break existing workspace `tasks/` files even though the high-level modes
exist.

Recommendation for 06:

- Add a `Task Contract Compatibility` subsection.
- State that the parser must load existing task YAML fixtures copied or
  redacted from `cc_workspace_bot` workspaces.
- Define the required legacy fields, default handling, and invalid state matrix
  in either 06 or a linked fixture doc.
- Define whether YAML or DB wins on conflict after startup, not only "restore
  from DB" and "fallback to scanning YAML".

#### 3.2 Attachment state lifecycle needs exact persistence semantics

v2 correctly includes attachments, but it does not state where pending
attachments are persisted. The original behavior matters because attachment-only
events may arrive before a later text instruction, and the process might restart
between those two events.

Risk: if pending attachments live only in memory, an ordinary restart loses the
attachment context. If they are stored in DB without cleanup semantics, stale
attachments can attach to future prompts.

Recommendation for 06:

- Define pending attachment storage explicitly: DB, filesystem marker, or both.
- Define expiration and cleanup behavior for pending but unconsumed attachments.
- Define whether attachment-only acknowledgement creates a session immediately
  or only a channel-level pending record.
- Add a test for restart between attachment-only and text events.

#### 3.3 `SESSION_CONTEXT.md` content may be too minimal

v2 includes app ID, workspace dir, session ID, channel key, routing key, sender
ID, task name, attachments dir, memory dir, tasks dir, and timestamp. This is a
good base, but it does not mention Feishu receive identity, message ID, chat
type, thread ID, workspace mode, or engine/thread identity.

Risk: legacy prompts and skills may rely on route details beyond a generic
channel key, especially for thread replies, task posts, and borrowed-channel
behavior.

Recommendation for 06:

- Add chat type, chat ID, thread ID, receive ID/type, message ID, workspace
  mode, engine thread ID when present, and task target fields.
- Define the file as framework-managed and overwritten per turn.
- Add a golden-file test for interactive, borrow-channel task, and system task
  contexts.

#### 3.4 Channel key rules may not match real receiver semantics

v2 defines P2P, group, and thread keys. This is useful, but the original design
only records `channel_key` as the stable worker key. The new formulas need to be
validated against actual `cc_workspace_bot` receiver behavior, especially for
Feishu group thread replies.

Risk: an altered channel key can fragment old sessions, fail `/new` against the
expected active session, or cause thread messages to share or split context
incorrectly.

Recommendation for 06:

- Add a requirement to derive the formulas from current `cc_workspace_bot`
  receiver code or fixture events.
- Add compatibility tests for P2P, group root, group thread, and repeated
  thread replies.

#### 3.5 Workspace init misses `.claude/story-state-*`

`02` explicitly says `.claude/story-state-*` must stay in place. v2 preserves
`.claude/skills`, `memory`, `tasks`, `sessions`, and `bot.db`, but does not call
out `.claude/story-state-*`.

Risk: implementation may treat `.claude/` as only skills and accidentally
ignore or disturb shared story-state files.

Recommendation for 06:

- Add `.claude/story-state-*` to workspace compatibility and workspace init
  tests.

### 4. Medium/Low-Priority Issues

- v2 says provider/model fields are retained for future config generation, but
  provider-specific behavior for `bailian` remains only a follow-up story. This
  is acceptable, but the scaffold should preserve enough metadata in logs/test
  fixtures to prove no provider information is dropped.
- Output segmentation says `[[SEND]]` is supported, but there is no exact
  rule for trimming, empty segments, or whether segmentation happens before or
  after filtering.
- `/new` is correct at session level, but tasks with `post_archive=true` need
  the same archive semantics specified against target channel state.
- The generated `AGENTS.md` bridge is required, but v2 should also reference the
  exact bridge content from `02` or define the required lines.
- The original startup initialized workspaces from `workspaces/_template`; v2
  preserves directories but does not discuss whether any template files should
  still be copied. This may be fine, but it should be explicit.

### 5. Suggested Additions To 06

- Add `Task Contract Compatibility` with legacy fixture requirements.
- Add `Attachment Persistence State Machine`.
- Add `SESSION_CONTEXT.md Golden Contract`.
- Add `.claude/story-state-*` to workspace compatibility.
- Add channel key compatibility tests derived from old receiver fixtures.
- Add `[[SEND]]` segmentation edge cases.
- Add explicit statement that no template files are copied into existing
  workspaces unless the file is framework-managed and missing.

## Review 2: Technical Architect

### 1. Overall Judgment

The revised architecture is now directionally sound. It has a clear local
service boundary, avoids AIPM FC/NAS three-state topology, and treats Codex
app-server as a separately designed package rather than burying protocol
details in the session path.

The main architecture risk has shifted from missing components to scope and
coupling control. v2 includes nearly every non-engine capability in one story.
That is consistent with the user's requirement, but it needs explicit internal
phasing and stronger interface contracts so implementation can remain
incremental without weakening acceptance.

### 2. Fixed Since The Previous Review

- `internal/codexapp` is now split into runtime manager, client, normalizer,
  approval adapter, interrupt adapter, and schema fixture.
- `session.Manager` is explicitly kept out of app-server protocol ownership.
- Configuration now uses a neutral `engine.type` and `codex.app_server`
  section.
- Queue bounds, idle timeout, duplicate message id idempotency, cancellation,
  and queue overflow behavior are included.
- Approval and interrupt are in the first scaffold interface.
- Debug API has local bind, disabled-by-config, body limit, app allowlist, and
  no arbitrary workspace path rules.
- Structured observability fields and lifecycle events are defined.

### 3. Remaining High-Priority Issues

#### 3.1 The architecture lacks a concrete store boundary

The runtime diagram ends in `Store`, and package design has `internal/db` plus
`internal/model`, but there is no repository/store abstraction. If session,
task, attachment, approval, and debug code all call GORM/SQLite directly, the
system will be harder to test and DB compatibility rules will scatter across
packages.

Recommendation for 06:

- Add `internal/store` or define repository interfaces under each domain.
- Make DB compatibility and additive migration live in one package.
- Require tests to run domain logic against a test store and legacy SQLite
  fixture.

#### 3.2 Engine event stream contract needs ownership and backpressure rules

v2 lists event types but not how streams are consumed, cancelled, buffered, or
converted into final output. This is especially important because app-server
streaming is the core future integration.

Recommendation for 06:

- Define whether `SendTurn` returns a channel, iterator, callback, or reader.
- Define event ordering rules: `turn_started` before deltas, exactly one
  terminal event, no deltas after terminal event.
- Define what happens on malformed event: terminal failure or ignored warning.
- Define cancellation ownership: context cancellation vs explicit
  `Interrupt`.
- Define stream buffer size and max event count interaction.

#### 3.3 Approval broker is underspecified as a domain component

v2 includes approval events and persistence, but there is no package for
approval records or state transitions. If approval later gets Feishu cards, this
state machine must already exist.

Recommendation for 06:

- Add `internal/approval` package or an explicit domain in `internal/engine`.
- Define states: requested, auto_allowed, auto_denied, pending_user,
  user_allowed, user_denied, expired, interrupted.
- Define timeout behavior and what happens to the active turn while approval is
  pending.

#### 3.4 App-server state directory isolation is not resolved

v2 says `RuntimeManager` owns cwd and `CODEX_HOME/state dir`, and config has
one `runtime_dir`. It does not decide whether state is shared globally, per app,
or per workspace.

Risk: shared app-server state can leak auth, threads, or working metadata across
apps. Per-turn state can break continuity. This is a core isolation decision.

Recommendation for 06:

- Define default `CODEX_HOME` or state path as per app or per workspace under
  framework runtime dir.
- State that app-server cwd is workspace root but framework runtime files live
  outside user workspaces unless they are session artifacts.
- Add tests that app A cannot read app B runtime state path via config.

#### 3.5 Story scope needs internal milestones

v2 deliverables include server, config, workspace, DB, Feishu mocks, session,
attachments, tasks, context, output, engine, mock engine, app-server skeleton,
debug API, and tests. That is compatible with "migrate all non-core-engine
capabilities", but implementation needs an acceptance sequence.

Recommendation for 06:

- Add a `Implementation Milestones` section:
  1. Config/workspace/DB/store fixtures.
  2. Feishu/debug dispatcher and channel/session loop.
  3. Context/output/mock engine.
  4. Attachments.
  5. Tasks.
  6. Approval/interrupt guardrails.
  7. Full legacy fixture and manual verification.

### 4. Medium/Low-Priority Issues

- `Guardrail Pipeline` appears in the diagram but has no package in package
  design. Either add `internal/guardrail` or make it part of
  `internal/session`/`internal/output`.
- `runtime_dir: ./runtime/codex` plus app workspaces under `/root` needs path
  normalization and root validation rules.
- `Engine.EnsureThread` is not obviously compatible with companion mode, where
  fresh thread per turn is required. The interface should distinguish
  `CreateThread`, `ResumeThread`, or make `EnsureThread` request policy
  explicit.
- `CloseThread` may not be meaningful in mock or app-server terms; specify it
  as "forget framework mapping" if app-server has no close primitive.
- `SchemaFixture` pins schema version, but the document should say tests fail
  on unknown event types unless explicitly allowed.
- `http.Server` appears as startup endpoint but Feishu real receiver is
  WebSocket-based in the old system. That is fine if `http.Server` only owns
  health/debug and lifecycle, but v2 should avoid implying real Feishu must be
  HTTP webhook.

### 5. Suggested Additions To 06

- Add store/repository boundary.
- Add engine stream ordering, terminal event, cancellation, and malformed event
  semantics.
- Add approval state machine and timeout behavior.
- Decide per-app/per-workspace app-server state directory isolation.
- Add implementation milestones inside the story.
- Add `internal/guardrail` or remove the package-level ambiguity.
- Clarify `EnsureThread` policy for work, companion, task, and `/new`.

## Review 3: Quality Engineer

### 1. Overall Judgment

v2 is now testable in principle. The automated verification matrix covers the
major compatibility risks called out in the previous review. It is much closer
to a real acceptance plan than v1.

The remaining quality risk is that several tests are listed by category but
will be hard to implement without fixtures, deterministic outputs, and precise
assertion targets. The design should specify the required test artifacts before
coding starts.

### 2. Fixed Since The Previous Review

- Acceptance is no longer "server starts". It requires `go test ./...` plus
  config, workspace init, DB, session, attachment, task, engine, HTTP/debug, and
  artifact tests.
- Manual verification now includes work mode, repeated messages, `/new`,
  companion fresh threads, attachment-only flow, three task modes, copied legacy
  workspace diff, malformed skill fixture, and redacted legacy config startup.
- Debug API side effects must be checked, not just HTTP status.
- Mock engine scenarios now include normal stream, usage, error, empty output,
  approval, interrupt, malformed event, and slow stream.
- Secret logging is addressed in config and Feishu/mock sender requirements.

### 3. Remaining High-Priority Issues

#### 3.1 Legacy fixtures are required but not defined

v2 requires a redacted legacy config fixture and a legacy DB fixture. It does
not define how those fixtures are produced, where they live, what must be
redacted, or which rows are mandatory.

Risk: tests may be created against artificial fixtures that do not represent
the live `cc_workspace_bot` state and therefore miss compatibility failures.

Recommendation for 06:

- Define fixture paths, for example `testdata/legacy/config.redacted.yaml`,
  `testdata/legacy/bot.db`, `testdata/legacy/tasks/*.yaml`, and
  `testdata/legacy/workspace/`.
- Define redaction rules: preserve shape, app count, provider/mode/model/tool
  fields; replace secrets with stable placeholders.
- Define required legacy DB rows: one channel, one active session with
  `claude_session_id`, one archived session, user/assistant messages, one task.

#### 3.2 Manual verification has no expected evidence artifacts

The manual checklist says what to verify, but not what evidence proves it. For
a future implementation handoff, we need repeatable evidence: command output,
SQLite query rows, mock sender log entries, filesystem diff paths, and warning
lines.

Recommendation for 06:

- Add expected evidence per manual step.
- Include example SQLite queries for session/thread reuse, `/new`, and message
  persistence.
- Include expected mock sender call sequences for work and companion modes.

#### 3.3 Idempotency and crash/restart scenarios are under-tested

v2 includes idempotency for duplicate message IDs and workspace init, but not
restart behavior for in-flight tasks, pending attachments, active turns, or
approval requests. These are common sources of production bugs.

Recommendation for 06:

- Add restart tests:
  - pending attachment survives or expires deterministically
  - unfinished task is marked failed/interrupted or recovered
  - pending approval is expired/interrupted on startup
  - active turn has a deterministic restart status

#### 3.4 Body/output limits need concrete values and test boundaries

v2 lists guardrails: message body size, output size, event count, max duration,
allowlist. It does not specify defaults or whether limits are configurable.

Risk: tests cannot reliably assert behavior, and the first implementation may
silently choose unsafe defaults.

Recommendation for 06:

- Put limit defaults in the config design.
- Add boundary tests at `limit`, `limit+1`, and missing config default.
- Define whether limit failures create DB messages and what user-facing reply is
  sent.

#### 3.5 `.gitignore` artifact test needs exact artifact list

v2 says generated DB/WAL/SHM, logs, pid files, runtime dirs, and debug
workspaces must be covered by `.gitignore`. It does not define file patterns or
whether workspace `bot.db` is ignored when inside a configured workspace.

Recommendation for 06:

- Define expected `.gitignore` patterns.
- Distinguish framework runtime artifacts from existing workspace state.
- Add a test or script that runs `git status --ignored --short` after debug
  dispatch and checks no unintended untracked runtime files remain.

### 4. Medium/Low-Priority Issues

- There is no coverage target or minimum for domain packages. Coverage number is
  not always useful, but requiring critical package tests avoids empty package
  shells satisfying the story.
- The debug endpoint scenario selection should have deterministic names and
  stable responses so manual verification can be scripted.
- Skill warning tests should assert warning structure, not only substring.
- Config redaction tests should cover nested maps/lists under old `claude`
  settings, not just top-level Feishu secrets.
- Output filter tests should cover no marker, one marker, multiple markers,
  leading/trailing markers, and empty segments.
- Task scheduler tests should use a fake clock rather than wall-clock sleeps.

### 5. Suggested Additions To 06

- Add `Test Fixtures` section.
- Add `Manual Evidence` section.
- Add `Restart And Recovery Tests`.
- Add concrete guardrail defaults and boundary tests.
- Add exact `.gitignore` artifact expectations.
- Require fake clock for task scheduler tests.
- Require deterministic debug scenario names and response schema.

## Consolidated Findings

### P0: Tighten Before Implementation

1. Add fixture definitions for redacted config, legacy DB, legacy task YAML, and
   copied workspace samples.
2. Define exact task YAML compatibility contract or link to fixture-driven
   validation.
3. Define attachment persistence state and restart behavior.
4. Expand `SESSION_CONTEXT.md` to include Feishu/message/thread/workspace-mode
   fields and add golden tests.
5. Add store/repository boundary so DB compatibility is not scattered.
6. Define engine stream ordering, terminal event, cancellation, and malformed
   event rules.
7. Define approval state machine and restart/timeout behavior.
8. Decide app-server state directory isolation per app/workspace.
9. Add implementation milestones inside the large scaffold story.

### P1: Add During The Same Design Pass If Possible

1. Add `.claude/story-state-*` preservation to workspace compatibility.
2. Validate channel key formulas against old receiver fixtures.
3. Specify `[[SEND]]` segmentation edge cases.
4. Clarify `EnsureThread` policy for work, companion, tasks, and `/new`.
5. Add guardrail default values and boundary tests.
6. Add manual verification evidence expectations.
7. Add exact `.gitignore` runtime artifact patterns.
8. Use fake clock for scheduler tests.

### P2: Acceptable Follow-Up Detail

1. Provider-specific app-server verification for Bailian.
2. Feishu approval card UI details.
3. Langfuse or equivalent tracing integration.
4. Full skill repair command for malformed Claude skills.

## Final Recommendation

Proceed with one more focused update to `06`, not another broad redesign. The
document is now structurally correct. The next update should make implicit
contracts concrete enough that implementation can be automated and reviewed
against fixtures, not interpretation.
