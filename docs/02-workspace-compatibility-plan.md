# Workspace Compatibility Plan

## Goal

All workspaces listed in the existing `/root/cc_workspace_bot/config.yaml`
should remain usable through `codex_workspace_bot` with minimal or no manual
workspace edits.

## Compatibility Rules

1. Read the legacy config file shape.
2. Do not mutate or delete `CLAUDE.md`.
3. Generate `AGENTS.md` only if missing.
4. Keep `.claude/story-state-*`, `.claude/skills`, `memory`, `tasks`,
   `sessions`, and `bot.db` in place.
5. Do not copy secrets into `~/.codex/config.toml`.
6. Write framework-managed generated files with clear headers.

## Workspace Init v1

For each configured app:

1. Ensure directories:
   - workspace root
   - `.codex/`
   - `.codex/skills/`
   - `.claude/skills/` for legacy scripts still used by prompts
   - `memory/`
   - `tasks/`
   - `sessions/`
2. Ensure `.memory.lock`.
3. Preserve existing `.claude/skills/feishu_ops/feishu.json` behavior for
   legacy scripts, with `0600` permissions.
4. If `AGENTS.md` is missing, create a generated bridge that tells Codex to
   read `CLAUDE.md` for legacy workspace instructions.

Generated bridge sketch:

```md
# Codex Workspace Bridge

This workspace was originally created for cc_workspace_bot.

Read `CLAUDE.md` as the legacy workspace instruction source, then follow
Codex-native rules from this file when they conflict.

Framework context for each turn is in `SESSION_CONTEXT.md`.
```

## Skills Migration

Do not bulk-convert all skills before first run.

v1 strategy:

- Keep `.claude/skills` available because existing workspace prompts reference
  those paths.
- Add `.codex/skills` only for new or repaired Codex-native skills.
- Detect malformed `SKILL.md` frontmatter/YAML and report a startup warning.
- Build a repair command later that converts common Claude skill formats into
  Codex-compatible skill frontmatter.

Known issue found during smoke test:

- `/root/course/.claude/skills/course-research/SKILL.md` has invalid YAML for
  Codex skill loading.

## Companion Workspaces

Companion mode has the highest migration risk.

Existing companion assumptions:

- Fresh Claude CLI session every turn.
- `RECENT_HISTORY.md` and related memory files provide continuity.
- Output filtering may depend on Claude Stop hooks and `FINAL_REPLY.md`.
- `[[SEND]]` controls multi-message delivery.

Codex v1 behavior:

- Preserve "fresh engine session every turn" for companion mode.
- Keep output filtering in Go where possible, not in engine hooks.
- Keep `[[SEND]]` segmentation in the session worker.
- Verify any existing companion filter hook before enabling it under Codex.

## Tasks

The task YAML contract can remain unchanged.

Codex app-server engine must support:

- user-facing task in a channel session
- borrow-channel task with `send_output=false`
- system task in `sessions/_system/<slug>/`
- `post_archive=true`

## Existing Workspace Inventory

Observed from current config without secrets:

- Total apps: 28
- Work mode: 21
- Companion mode: 7
- Providers: `anthropic`, `bailian`
- Existing workspace roots include `/root/course`, `/root/health`,
  `/root/investment`, `/root/ycm_mate`, `/root/mango_daxian`,
  `/root/dqh_investment`, and others under `/root`.

## Compatibility Acceptance Tests

Minimum local tests before running the live bot:

1. Config loads with all 28 apps and no secret logging.
2. Workspace init is idempotent for all 28 workspaces.
3. Existing `CLAUDE.md` files are not changed.
4. Missing `AGENTS.md` files get bridge files.
5. A work-mode prompt produces a Codex JSONL result.
6. A resume prompt uses the prior `thread_id`.
7. A companion prompt starts fresh and still reads memory.
8. A task prompt runs in `sessions/_system/<slug>/`.
9. Feishu send/update paths are unchanged.
10. Malformed skills are reported with app/workspace path.
