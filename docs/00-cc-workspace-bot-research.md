# cc_workspace_bot Research

Date: 2026-06-28

Source project: `/root/cc_workspace_bot`

## Executive Summary

`cc_workspace_bot` is a single-machine Feishu AI assistant platform. Its
reusable layers are Feishu WebSocket receiving/sending, app routing, per-channel
session workers, SQLite state, task scheduling, attachment handling, and
workspace initialization. Its Claude-specific layer is concentrated mostly in
`internal/claude`, plus workspace templates and `.claude/skills` assumptions.

The Codex rewrite should not start by replacing every module. The fastest safe
path is to introduce a neutral streaming engine interface, implement a Codex
app-server engine, and keep the existing orchestration model until compatibility
is proven.

## Current Runtime Shape

Startup flow in `cmd/server/main.go`:

1. Load `config.yaml`.
2. Initialize every workspace directory from `workspaces/_template`.
3. Create one SQLite DB per workspace.
4. Construct one CLI executor.
5. Start one Feishu WebSocket receiver per app.
6. Start session manager and task subsystem.
7. Watch each workspace `tasks/` directory.
8. Serve `/health`.

Message flow:

1. Feishu event is received.
2. Receiver downloads attachments and builds an `IncomingMessage`.
3. Session manager routes by `channel_key`.
4. A single worker serializes messages for that channel.
5. Worker creates or reuses an active session.
6. Worker writes user message to DB.
7. Work mode sends a thinking card; companion mode sends plain text later.
8. Executor invokes `claude`.
9. Worker persists assistant output and sends or patches Feishu response.

## Important Existing Behaviors

- `channel_key` is the stable worker key.
- `/new` archives the active session and clears CLI context continuity.
- Work mode uses a Feishu thinking card, then patches it.
- Companion mode starts a fresh Claude session every turn and relies on
  workspace memory/history files for continuity.
- Attachment-only messages are acknowledged and cached until the user sends a
  text instruction.
- Scheduled tasks are YAML source-of-truth mirrored into DB.
- System tasks can run without a chat target in `sessions/_system/<slug>/`.
- `SESSION_CONTEXT.md` is written into each session cwd before execution.
- Routing metadata is injected into prompt as `<system_routing>`.

## Claude-Specific Binding Points

Main file: `internal/claude/executor.go`.

Current command shape:

```text
claude -p <prompt>
  --output-format stream-json
  --verbose
  --permission-mode <permission_mode>
  --max-turns <n>
  --add-dir <workspace>
  --model <model>
  --effort <effort>
  --settings <json>
  --resume <claude_session_id>
  --allowedTools <tools>
```

Current parser expects:

- `system.session_id` as CLI session ID.
- `assistant.message.content[].text` as output chunks.
- `result.cost_usd`, `result.duration_ms`, `result.is_error`,
  `result.result`.

Current environment handling:

- Filters inherited `CLAUDECODE`, `CLAUDE_CODE_`, `WORKSPACE_DIR`,
  `ANTHROPIC_`, `CC_LF_`.
- Adds `WORKSPACE_DIR`.
- Adds `ANTHROPIC_*` for alternate providers such as Bailian.
- Adds `CC_LF_*` for Langfuse hooks.

Claude-specific recovery:

- Sanitizes Claude resume JSONL under `~/.claude/projects/...` when foreign
  provider output pollutes Anthropic resume history.

This recovery path is not directly portable to Codex and should be removed from
the generic engine interface.

## Existing Config Shape

Current live config has 28 apps.

Workspace modes:

- `work`: 21 apps.
- `companion`: 7 apps.

Providers:

- `anthropic`: default for most apps.
- `bailian`: used by at least `xiao-buz` and `mango_daxian`.

Global settings observed without copying secrets:

- `timeout_minutes`: 90.
- `max_turns`: 300.
- `default_provider`: `anthropic`.

Most apps define:

- `workspace_dir`
- `workspace_mode`
- `claude.permission_mode`
- `claude.allowed_tools`
- optional `claude.model`
- optional `claude.provider`

## Workspace Layout Assumptions

Existing workspaces generally contain:

- `CLAUDE.md`
- `.claude/settings.local.json`
- `.claude/skills/...`
- `memory/`
- `tasks/`
- `sessions/`
- `bot.db`

Codex-compatible workspace support must account for:

- `AGENTS.md` is the Codex project guidance file.
- Codex may also discover and try to load skills from `.claude/skills`.
- Some existing Claude skills may have frontmatter/YAML that Codex rejects.
- Existing secrets in `.claude/skills/feishu_ops/feishu.json` must not be
  copied into general Codex config.

## Codex App-Server Findings

The migration target is Codex app-server. App-server provides a long-lived
runtime with thread/turn events, approval requests, and interrupt/steer control.
The bot should normalize that stream into its own engine events and keep
approval/guard decisions in the bot process.

## Main Migration Risk Areas

1. Session continuity: map `claude_session_id` column to a neutral
   `engine_session_id` concept while keeping DB compatibility.
2. Permission semantics: Claude `permission_mode` and `allowed_tools` do not map
   1:1 to Codex sandbox/approval/rules.
3. Workspace instructions: existing `CLAUDE.md` must be bridged into
   `AGENTS.md` without destroying user edits.
4. Skills: `.claude/skills` may load partially in Codex but malformed YAML can
   produce startup errors.
5. Provider support: Bailian Anthropic-compatible bridge must be verified
   through the app-server configuration path.
6. Companion output hooks/filtering: old design relies on Claude hooks and stop
   behavior; Codex hook equivalents need verification.
7. Langfuse: old `CC_LF_*` hook attribution does not automatically apply.
