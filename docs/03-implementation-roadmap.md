# Implementation Roadmap

## Phase 0: Preserve Knowledge

Status: started.

Deliverables:

- Research docs under `docs/`.
- New project skeleton.
- No secret migration.

## Phase 1: Mechanical Port With Engine Boundary

Goal: compile a bot that is structurally equivalent to `cc_workspace_bot` but
uses `internal/engine` and is ready for streaming app-server events.

Steps:

1. Copy or port reusable packages:
   - `config`
   - `db`
   - `feishu`
   - `model`
   - `session`
   - `task`
   - `workspace`
2. Add `internal/engine`.
3. Keep a temporary Claude executor adapter only if needed for parity tests.
4. Change session/task dependencies from concrete Claude executor to
   `engine.Engine`.
5. Keep DB schema unchanged by mapping existing `claude_session_id` to
   `engine_thread_id`.
6. Change workers from "wait for final ExecuteResult" to "consume turn event
   stream and assemble final output".

Verification:

```text
go test ./...
go build ./cmd/server
```

## Phase 2: Codex App-Server Runtime

Goal: support one work-mode app through Codex app-server.

Steps:

1. Generate and pin app-server JSON schema for the target Codex version.
2. Implement app-server process lifecycle and transport setup.
3. Implement initialize/thread/turn protocol client.
4. Normalize app-server notifications into `engine.Event`.
5. Write `SESSION_CONTEXT.md`.
6. Inject routing context into turn input.
7. Parse deltas, approval requests, usage, and completion events.
8. Run smoke test against one low-risk work-mode workspace.

Key test fixtures:

- schema contains turn events, approval requests, interrupt, and steer
- thread start/resume
- `item/agentMessage` delta stream
- approval request and bot denial
- `turn/completed` usage
- `turn/interrupt`
- malformed legacy `.claude/skills` startup behavior

## Phase 3: Approval Broker and Hook Replacement

Goal: replace Claude/Codex hook assumptions with bot-owned guardrails.

Steps:

1. Add approval broker package.
2. Map command/file/permission/tool approval requests into policy decisions.
3. Auto-deny known dangerous commands and cross-chat Feishu sends.
4. Auto-allow low-risk reads where policy allows.
5. Add Feishu approval-card flow for cases that need human approval.
6. Implement max-turn/tool-count interrupt.
7. Add final-output empty/error/moderation guard.

## Phase 4: Workspace Compatibility Init

Goal: initialize all existing workspaces safely.

Steps:

1. Add generated `AGENTS.md` bridge for workspaces missing it.
2. Add malformed skill scanner.
3. Preserve existing `.claude/skills/feishu_ops/feishu.json` credential write.
4. Add dry-run mode for init.

Verification:

```text
codex_workspace_bot init --config /root/cc_workspace_bot/config.yaml --dry-run
codex_workspace_bot init --config /root/cc_workspace_bot/config.yaml
```

## Phase 5: Live Feishu Compatibility

Goal: one safe test app can respond through Feishu.

Steps:

1. Start server with copied legacy config path.
2. Pick one low-risk work-mode app.
3. Send plain text message.
4. Test attachment-only then text merge.
5. Test `/new`.
6. Test one task YAML.

## Phase 6: Companion Compatibility

Goal: companion apps keep current user experience.

Steps:

1. Decide whether companion uses fresh app-server thread per turn or persistent
   app-server process with fresh thread IDs.
2. Port output filter logic into Go where possible.
3. Verify `[[SEND]]` segmentation.
4. Verify memory and recent history update behavior.
5. Run one companion workspace in shadow mode before live cutover.

## Phase 7: Provider and Policy Cleanup

Goal: replace compatibility shortcuts with explicit Codex-native configuration.

Steps:

1. Decide provider strategy for Bailian.
2. Add Codex profiles or rules for production execution.
3. Replace `claude:` config name with `engine:` or `codex:` while accepting
   legacy config.
4. Add migration command that writes a sanitized new config.

## Immediate Open Questions

1. Should v1 use a copy of `/root/cc_workspace_bot/config.yaml`, or read it in
   place during compatibility rollout?
2. Should the first live app be `/root/course` or a lower-risk workspace?
3. Is Bailian support required in the first live milestone, or can Anthropic
   apps launch first?
4. Should malformed `.claude/skills` be repaired automatically, or only reported
   until each workspace is tested?
5. Should app-server processes be one per app workspace, one per active session,
   or a shared pool keyed by app ID?
6. Which transport is production default: `unix://` local socket or loopback
   `ws://` with capability token?
