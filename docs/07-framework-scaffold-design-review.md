# Framework Scaffold Design Review

Date: 2026-06-28

Reviewed document:

- `docs/06-framework-scaffold-story-design.md`

Reference documents:

- `docs/00-cc-workspace-bot-research.md`
- `docs/02-workspace-compatibility-plan.md`
- `docs/05-aipm-codex-appserver-research.md`

## Executive Summary

The three reviewers agree that `06-framework-scaffold-story-design.md` is a
valid starting point, but it is currently too thin for the stated goal of
rebuilding `cc_workspace_bot` as a Codex app-server based bot while preserving
existing workspaces.

The current design describes a minimal runnable scaffold. It does not yet
encode enough compatibility-critical behavior from `cc_workspace_bot`, nor does
it define a strong enough app-server boundary for later replacement of the mock
engine.

Before implementation, `06` should be revised into a more complete
implementation design. The highest-priority fixes are:

1. Add task subsystem scope.
2. Add companion-mode semantics.
3. Add attachment and `SESSION_CONTEXT.md` handling.
4. Add old DB compatibility rules.
5. Split the app-server abstraction into runtime/client/event/approval layers.
6. Add measurable automated and manual acceptance criteria.

## Review 1: CC Workspace Bot Technical Lead

### Overall Judgment

The design keeps the basic runtime shape of `cc_workspace_bot`: multi-app
config, workspace init, SQLite, channel workers, `/new`, message persistence,
thinking card behavior, and mock engine boundaries.

However, it is closer to a demo scaffold than a compatibility scaffold. If
implemented as written, it may run a simple debug message but fail to preserve
important behavior for the existing 28 configured workspaces.

### High-Priority Issues

- **Task subsystem is missing.** The original bot starts the task subsystem,
  watches `tasks/`, mirrors YAML into DB, and supports user-facing,
  borrow-channel, system, and `post_archive=true` tasks. `06` does not include
  this in borrowed capabilities or runtime architecture.

- **Companion mode is underspecified.** Existing companion workspaces start a
  fresh engine session every turn and rely on workspace memory/history for
  continuity. `06` only says companion mode sends direct text.

- **Attachment behavior is missing.** The original receiver downloads
  attachments, supports attachment-only acknowledgement, and caches pending
  attachments until a text instruction arrives.

- **`SESSION_CONTEXT.md` is missing.** Existing skills rely on this file for
  session, routing, attachment, and task context. `06` mentions routing only
  implicitly through the turn flow.

- **DB compatibility is not concrete.** Existing `bot.db` files and the
  physical `claude_session_id` field need an additive compatibility strategy.
  The Go model can expose a neutral engine/thread name, but the old column must
  remain readable and writable during migration.

### Medium/Low-Priority Issues

- Preserve the per-app Feishu receiver model even if this story starts with a
  debug endpoint.
- Define `SendText`, `SendThinking`, and `UpdateCard` as the sender contract.
- Preserve `.claude/skills` and `feishu_ops/feishu.json` permissions during
  workspace init.
- Keep legacy provider fields such as `anthropic` and `bailian` without logging
  secrets.
- Preserve observability fields for future Langfuse or equivalent tracing.
- Define `/new` as archive plus clearing engine continuity, not only creating a
  new session.

## Review 2: Technical Architect

### Overall Judgment

The app-server direction is correct and does not copy AIPM's FC/NAS three-state
sandbox. The document correctly targets a local Go service with a mock engine
first.

The architecture is still incomplete. It compresses the future app-server
integration into a single thin `engine.Engine` boundary, which risks leaking
protocol, approval, and runtime lifecycle concerns into `session.Manager`.

### Architecture Gaps

- **App-server boundary is too thin.** A real implementation needs separate
  components for runtime lifecycle, protocol client, event normalizer, approval
  broker, interrupt/steer controller, and schema compatibility.

- **Configuration model is inconsistent.** Prefer a split model:

  ```yaml
  engine:
    type: mock # mock | codex-app-server

  codex:
    app_server:
      listen: unix
      auth: capability-token
      approval_policy: untrusted
      schema_version: "0.142"
  ```

- **Neutral engine identity is missing.** The design should state how
  `engine_thread_id` or `engine_session_id` maps to old `claude_session_id`.

- **Workspace layering is incomplete.** The design should define app-server cwd,
  per-app `CODEX_HOME` or state directory, global/workspace/session layers, and
  how `CLAUDE.md`, `AGENTS.md`, `.claude/skills`, memory, and session context
  are bridged.

- **Concurrency and recovery rules are incomplete.** Add queue bounds, worker
  lifecycle, duplicate Feishu event idempotency, cancellation, active-turn
  constraints, interrupt behavior, and restart handling for unfinished tasks.

- **Observability is not designed.** Add request/event/channel/session/thread
  identifiers and structured turn lifecycle events.

### Architecture Risks

- `session.Manager` could become a large untestable orchestrator if it owns
  protocol normalization, approval, guardrails, Feishu card updates, and session
  lifecycle all at once.

- A happy-path mock engine can hide real app-server complexity. The mock must
  emit delta, completed, failed, approval request, interrupted, empty output,
  and malformed event scenarios.

- `/debug/dispatch` must be local-only or protected, body-limited, and disabled
  in production.

- Approval and interrupt must be present in first-version interfaces even if
  Feishu approval cards are implemented later.

## Review 3: Quality Engineer

### Overall Judgment

The current acceptance criteria prove that the scaffold can start and handle a
simple debug path, but they do not prove compatibility with the original bot.
Many high-risk compatibility behaviors are not converted into tests or manual
checks.

### Quality Risks

- `/debug/dispatch` could return success while DB side effects are wrong.
- `/new` is listed as borrowed behavior but has no acceptance test.
- Companion mode could accidentally reuse engine thread state.
- Workspace init could overwrite `CLAUDE.md` or `AGENTS.md`.
- The existing 28-app config is not included in a compatibility fixture.
- Task and attachment behavior are neither included nor explicitly deferred.
- `SESSION_CONTEXT.md`, `<system_routing>`, and malformed skill warnings lack
  automated checks.
- Mock engine event coverage is not defined.

### Automated Test Recommendations

- Config loading tests for demo config and a redacted legacy config fixture.
- Workspace init idempotency tests with existing `CLAUDE.md`, existing
  `AGENTS.md`, `.claude/skills`, `memory`, `tasks`, `sessions`, and `bot.db`.
- DB migration/compatibility tests for channels, sessions, messages, tasks, and
  old engine/session columns.
- Session routing tests for same channel serialization and different channel
  isolation.
- `/new` tests that verify archive and new engine thread identity.
- Work-vs-companion tests for card calls and fresh companion engine thread.
- Mock engine contract tests for streaming, completion, errors, approval, and
  interrupt.
- HTTP integration tests that inspect DB side effects, not only HTTP status.
- Skill warning tests for malformed `SKILL.md`.

### Manual Verification Recommendations

- Start from checked-in template and confirm `/health` is stable and logs do not
  contain secrets.
- Send work-mode debug messages and inspect mock card logs plus SQLite rows.
- Send repeated messages in one chat and confirm session/thread reuse.
- Send `/new` and confirm old session archived and new thread identity.
- Send companion-mode messages and confirm no engine thread reuse.
- Run workspace init against a copied legacy workspace and diff protected files.
- Start with malformed skill input and confirm non-fatal warnings.
- Start with a redacted legacy config and confirm all apps initialize without
  secret logging.

## Consolidated Required Revisions To `06`

### P0: Must Fix Before Implementation

1. **Add compatibility-critical behavior section**
   - Stable channel key rules.
   - `/new` archives active session and clears engine continuity.
   - `SESSION_CONTEXT.md` is generated before every turn.
   - `<system_routing>` is injected.
   - Work mode and companion mode semantics are separate.

2. **Add task subsystem v1 scope**
   - YAML scan from `tasks/`.
   - DB mirror.
   - Scheduler registration.
   - User-facing, borrow-channel, and system task modes.
   - `sessions/_system/<slug>/`.
   - `send_output=false` and `post_archive=true`.
   - Engine execution can be mocked, but task orchestration should not be
     omitted from the scaffold design.

3. **Add DB compatibility section**
   - Continue using each workspace's `bot.db`.
   - Additive migrations only.
   - Preserve old tables and columns.
   - Map physical `claude_session_id` to a neutral engine thread/session field.

4. **Add app-server architecture boundary**
   - `internal/engine`: neutral event and command contracts.
   - `internal/codexapp`: runtime manager, protocol client, event normalizer,
     schema fixture, approval/interrupt adapter.
   - `internal/mockengine`: deterministic mock with realistic event scenarios.

5. **Add workspace init compatibility**
   - Never mutate `CLAUDE.md`.
   - Generate `AGENTS.md` only when missing.
   - Preserve `.claude/skills`, `memory`, `tasks`, `sessions`, `bot.db`.
   - Detect malformed skills and warn with app/workspace path.
   - Preserve secret-bearing legacy files without copying them into Codex config.

### P1: Should Fix In The Same Design Pass

1. **Add attachment model**
   - `IncomingMessage.Attachments`.
   - Attachment-only acknowledgement.
   - Pending attachment cache.
   - Debug API accepts mock attachments.

2. **Add companion output contract**
   - Fresh engine thread per turn.
   - Memory/history continuity through workspace files.
   - `[[SEND]]` segmentation.
   - Go-side filter interface replacing hook-dependent behavior.

3. **Add sender contract**
   - `SendText`.
   - `SendThinking`.
   - `UpdateCard`.
   - Mock sender must record calls for tests.

4. **Add concurrency/recovery policy**
   - One worker per channel.
   - Bounded queue.
   - Worker idle timeout.
   - Duplicate message id idempotency.
   - One active turn per channel.
   - Context cancellation and interrupt path.
   - Restart handling for unfinished tasks.

5. **Add observability**
   - Structured log fields: app id, channel key, session id, engine thread id,
     message id, task id.
   - Turn lifecycle events: started, delta, approval requested, interrupted,
     completed, failed.
   - Usage and duration metrics.

6. **Add debug API safety**
   - Localhost-only by default.
   - Disabled in production unless explicitly enabled.
   - Body size limits.
   - App id must exist in config.
   - No arbitrary workspace path input.

### P2: Can Be Deferred But Must Be Explicit

1. Real Feishu WebSocket receiver and SDK sender.
2. Real app-server protocol client.
3. Feishu approval card UI.
4. Full attachment download from Feishu.
5. Provider-specific app-server runtime verification for Bailian.
6. Langfuse or equivalent tracing integration.

## Proposed Revised Acceptance Criteria

The implementation story should not be accepted only because the server starts.
The revised acceptance criteria should include:

- `go test ./...` passes with coverage for config, workspace init, DB
  compatibility, session routing, `/new`, mock engine stream, task contracts,
  and debug API.
- Server starts from a checked-in template and `/health` returns stable JSON.
- `/debug/dispatch` writes user message, assistant message, active session, and
  engine thread id to SQLite.
- Same app/chat/thread messages are serialized; different channels do not block
  each other.
- `/new` archives the current active session and the next turn uses a new
  engine thread identity.
- Work mode calls `SendThinking` then `UpdateCard`; companion mode sends direct
  text and does not reuse previous engine thread.
- Workspace init is idempotent and does not modify existing `CLAUDE.md` or
  `AGENTS.md`.
- Legacy workspace directories and `bot.db` are preserved.
- A redacted legacy config fixture loads all expected apps and preserves
  workspace mode, provider, and legacy `claude` fields.
- Malformed skills produce non-fatal warnings with app id and workspace path.
- Mock engine supports at least delta, completed, error, approval request, and
  interrupt acknowledgement events.
- Runtime artifacts are ignored by git.
- The design explicitly lists which old capabilities are deferred and where
  they will be implemented.

## Final Recommendation

Do not implement from the current `06` as-is.

First revise `06-framework-scaffold-story-design.md` into a stronger scaffold
implementation spec. The next version should treat compatibility behavior and
app-server orchestration as first-class design constraints, while still keeping
the real engine and Feishu SDK mocked for the first runnable story.
