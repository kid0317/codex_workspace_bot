# Decisions

## D001: Keep Legacy Config Readability

Decision: v1 must read the existing `cc_workspace_bot` config shape, including
the `claude:` section name.

Reason: the live config has 28 apps. Requiring a config rewrite before first
run creates unnecessary migration risk.

## D002: Introduce Engine Boundary Before Full Rename

Decision: use a neutral `internal/engine` interface before renaming every
Claude-specific identifier.

Reason: session, task, Feishu, DB, and workspace orchestration are largely
reusable. A broad rename before behavior parity would make regressions harder
to isolate.

## D003: Generate AGENTS.md Bridges, Preserve CLAUDE.md

Decision: generated Codex compatibility files may be added, but existing
`CLAUDE.md` files must not be modified during automatic workspace init.

Reason: existing workspace prompts are user-authored operational knowledge.
Codex compatibility should be additive and reversible.

## D004: App-Server Is The First Engine

Decision: this phase will implement Codex app-server as the primary engine.

Reason: the user explicitly wants app-server in this phase. AIPM POC_R1 proves
the app-server protocol has the streaming, approval, interrupt, and steer
capabilities needed to replace Claude hooks and support Feishu streaming UX.

## D005: Do Not Copy AIPM FC/NAS Three-State Sandbox

Decision: borrow only AIPM's POC results, streaming protocol model, and
approval/interrupt guardrail pattern. Do not copy FC Session lifecycle, NAS
mount topology, Dev/Use/Eval state model, or enterprise/scene/version routing.

Reason: `codex_workspace_bot` is a local multi-workspace Feishu bot. Its
compatibility problem is preserving existing workspace directories and config,
not building a cloud sandbox platform.

## D006: Bot Orchestrator Owns Guardrails

Decision: user-facing send, approval, max-turn, and final-output guards belong
in the bot process, not inside workspace prompt instructions or Codex hook
scripts.

Reason: AIPM's app-server research found headless hooks are not a reliable
enforcement boundary, while app-server event streams and approval requests give
the orchestrator a deterministic interception point.

## D007: Pin And Test The App-Server Protocol

Decision: app-server support must start from a generated JSON schema fixture for
the target Codex version and protocol-level tests.

Reason: app-server is experimental and schema-sensitive. AIPM is useful because
its POC proves the capability class, but this project must verify the exact
local Codex version and command-line behavior before depending on it.
