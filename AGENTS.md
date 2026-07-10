# Codex Workspace Bot Project Guidance

## Project Context

This repository is a Codex-native rewrite of the previous local Feishu workspace bot. Treat it as a local Codex Feishu bot/orchestrator, not as a generic chat webhook service.

Core design sources:

- `docs/01-codex-appserver-protocol-research.md`
- `docs/02-redesign-high-level.md`

Read those docs before making architecture, runtime, protocol, or storage changes.

## AI-Native Delivery Model

- This repository is built as a living AI work environment, not merely source code. Keep project knowledge, executable workflow, deterministic gates, and post-change learning close to the code and under version control.
- Start every Story from a compact, reviewable artifact that states: goal/background, in-scope and out-of-scope behavior, hard constraints, scenario-based acceptance tests, risks, and decisions requiring a human. Do not treat prose alone as acceptance.
- Design scenario tests before implementation. Preserve their intent: a failing test may reveal a code defect, an outdated test, or an incomplete requirement; never weaken or delete an assertion merely to make a run pass.
- Follow the delivery loop: Spec alignment -> scenario tests -> detailed design -> TDD implementation -> independent review -> integration/security checks -> evidence-based release decision -> retrospective.
- A Story is not complete merely because code merged. Record the evidence that supports the release decision and the residual risks/owners. Local fakes and static fixtures prove only their stated local gate.
- After a meaningful Story, inspect its diff, test/CI results, review feedback, runtime traces, and human corrections. Classify a recurring failure as missing knowledge, SOP, rule, tool, automated gate, or obsolete context; add the smallest durable improvement and remove stale or duplicate Harness content.
- Keep agent context selective. Prefer a focused code map, interface/document index, and task-specific references over loading large repositories or repeating all project knowledge in `AGENTS.md`.
- Do not create a code map, task-specific Skill, Hook, CI gate, or generated project knowledge merely because this mechanism exists. Create one only when implementation creates the corresponding stable boundary, repeated workflow, or verifiable failure mode; give it an owner, a trigger, and a validation path.
- When implementation starts, maintain project knowledge through the smallest useful artifact: create a code map for stable package relationships, a Skill for a repeated judgment-heavy workflow, a deterministic gate for enforceable checks, and a tool integration only for approved external capabilities.

## Product Direction

- Build only the Codex-native version. Do not add Claude compatibility, engine abstraction, or a multi-engine fallback layer unless the user explicitly changes the project direction.
- Use one long-lived `codex app-server --stdio` process to serve multiple workspaces.
- Route workspaces through App Server `thread/start` and `turn/start` `cwd`; do not start one App Server per workspace as the default design.
- Treat a Feishu App as the top-level product app, a Feishu chat/channel as the serial scheduling unit, and a Codex Thread as the session backing one active channel session.
- Keep same-channel messages strictly serialized. Different channels may run in parallel through separate workers.

## Runtime Contracts

- App Server transport is newline-delimited JSON-RPC over stdio.
- Always call `initialize` before any other App Server method.
- Keep one concurrency-safe App Server client per process. It must have one serialized stdout writer, correlate every response by its JSON-RPC request ID, and route notifications and server requests by `thread_id` and `turn_id`; do not let workers write directly to process stdin.
- Prefer these core methods for runtime behavior:
  - `thread/start`
  - `thread/resume`
  - `thread/archive`
  - `turn/start`
  - `turn/interrupt`
  - `turn/steer`
  - `account/rateLimits/read`
  - `account/usage/read`
- Persist the returned Codex thread ID on the local session record.
- Pass the validated app `workspace_dir` on every `thread/start`, `thread/resume`, and `turn/start`. `turn/start.cwd` affects subsequent turns, so never rely on an inherited cwd.
- On App Server exit, mark in-progress local turns as failed, restart the process, and do not replay the interrupted message automatically. On a later message, try `thread/resume`; on failure, start a new thread, persist its ID, and record `resume_fallback_started_new_thread`.
- `/new` interrupts an active turn if needed, archives and clears the current local session, then replies immediately. Create the new Thread lazily on the next non-command message.
- `/cancel` and `/stop` are idempotent aliases and must use `turn/interrupt` rather than killing the App Server process. `/status` reads account rate-limit and usage data; `/help` is static.

## Data And Concurrency

- The intended storage layer is MySQL plus in-process queues.
- Do not introduce Redis, SQLite, or PostgreSQL as the default persistence path.
- Worker queues are in-memory and channel-scoped.
- Preserve the design defaults unless the docs are updated:
  - max active workers: 20
  - per-worker queue depth: 64
  - idle worker recycle: 30 minutes
- Treat `channel_key = "{chat_type}:{chat_id}:{app_id}"` as the routing identity.
- Different Feishu Apps may share the same process but must remain isolated by app config, credentials, workspace directory, and allowed chat rules.
- Enforce receipt/idempotency checks before both enqueueing and out-of-band control-command handling. On a full queue or saturated worker pool, return a deterministic user-facing rejection; never silently drop a message.

## Feishu Behavior

- Feishu WebSocket events are the primary ingress path.
- Each Feishu App has its own receiver/client context and credentials.
- Validate `AllowedChats` before enqueueing user messages.
- Support text first; when attachments are implemented, download them into session-scoped attachment storage before passing file inputs to Codex.
- Work mode should stream App Server deltas into Feishu cards.
- Companion mode should send plain text segments and use `[[SEND]]` as the segmentation marker.
- Card buttons for cancel/retry/approval must route back to the owning worker/session, not to a global loose handler.
- Persist a pending approval before sending its card. Bind the callback to app, channel, session, thread, turn, request ID, and an expiring nonce; resolve the matching App Server request exactly once. Approval timeout must deny and interrupt the turn.

## Observability

- Langfuse integration belongs at the orchestrator/App Server notification layer.
- Prefer parsing App Server notifications directly over shell hooks.
- At minimum, preserve and log:
  - Feishu app/channel/message identifiers
  - local session ID
  - Codex thread ID
  - active turn ID
  - model/effort
  - final status/error
  - token usage events when available
- Do not store secrets, raw tokens, or full sensitive message payloads in observability metadata unless the user explicitly asks for a local debugging artifact.
- Treat Langfuse as metadata-only and fail-open. Use hashed identifiers with a configured salt; do not export prompts, attachment contents, credentials, raw chat IDs, or raw approval details.

## Instruction Loading

- Codex global instructions come from `CODEX_HOME/AGENTS.md`.
- Project/workspace instructions come from the current `cwd`; this project expects Codex to support existing workspace instruction files through configured fallback filenames such as `CLAUDE.md`.
- Do not create bridge files in every workspace unless a concrete runtime check proves they are needed.
- When debugging instruction loading, verify with Codex tooling or App Server responses instead of guessing from file names.

## Go Implementation Standards

- Use the Go version declared by `go.mod`; format all Go with `gofmt` and keep `go vet ./...` clean.
- Organize packages by the designed boundaries: `feishu`, `router`, `session`/worker, `codexapp`, `storage`, `command`, `output`, and `observability`. Avoid generic utility packages and avoid cyclic dependencies.
- Define interfaces at the consuming boundary for external systems (App Server, Feishu, MySQL, clock) and keep production adapters small. Do not create interfaces for local implementation details without a test or alternate implementation need.
- Thread `context.Context` from ingress through storage and App Server calls. Do not retain request contexts in long-lived workers; derive worker and turn contexts with explicit cancellation and deadlines.
- Return typed or sentinel errors only when callers branch on them. Otherwise wrap operational failures with `%w` and enough stable context to identify the operation, never secrets or raw user content.
- Make ownership explicit for mutable state. A channel worker exclusively owns its queue, active session, active turn, and card update state; shared registries use synchronization and must not expose mutable internals.
- Validate configuration at startup: unique App IDs, non-empty workspace directories, valid mode and approval policy, bounded worker settings, and per-App credential/allowed-chat isolation. Keep credential-bearing local configuration out of version control.
- Use parameterized SQL and versioned, forward-only MySQL migrations. Keep persistence changes compatible with active sessions and make state transitions conditional/idempotent.
- Redact before writing logs, MySQL message records, Langfuse events, or Feishu output. Attachment paths must remain session-scoped and cleanup must be bounded by configured retention.
- Prefer `slog` with stable fields such as `app_id`, `channel_key`, `session_id`, `thread_id`, `turn_id`, `event`, and `error`; do not log secrets, authorization headers, raw tokens, or full message bodies.
- Keep JSON-RPC schema/protocol types explicit. Treat server requests, notifications, responses, and errors as distinct message classes; server requests always receive a protocol-correct response, while notifications never do.

## Test And Change Gates

- Add focused tests for protocol parsing and request correlation, same-channel serialization, queue saturation, receipt deduplication, command idempotency, thread-resume fallback, approval round trips/timeouts, and Feishu card send/update failures.
- Use fake App Server and fake Feishu clients for unit and module tests. Keep real App Server and Feishu smoke tests explicit, separately configured integration gates.
- For concurrent code, run targeted race coverage before considering the change complete. Do not claim full acceptance from local fakes or static evidence; state any remaining live Feishu, Codex, observability, or operations gate explicitly.
- Update the two design documents whenever a change alters an accepted runtime, storage, protocol, security, or concurrency contract. Otherwise keep implementation changes focused and do not widen product scope.

## Current Repository State

At the time this guidance was written, the repository contains design docs only. Do not assume previous scaffold code exists unless it is present in the current checkout.
