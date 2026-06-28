# Codex Engine Migration Plan

## Goal

Replace the Claude Code engine with Codex app-server while preserving the
existing bot orchestration model.

The first production implementation is app-server mode.

## Target Interface

Introduce a neutral streaming engine package:

```go
type Engine interface {
    EnsureThread(ctx context.Context, req *EnsureThreadRequest) (*Thread, error)
    SendTurn(ctx context.Context, req *TurnRequest) (<-chan Event, error)
    RespondApproval(ctx context.Context, req *ApprovalResponse) error
    Interrupt(ctx context.Context, req *InterruptRequest) error
    CloseThread(ctx context.Context, threadID string) error
}

type EnsureThreadRequest struct {
    AppConfig      *config.AppConfig
    WorkspaceDir   string
    SessionDir     string
    SessionID      string
    EngineThreadID string
    Model          string
    Mode           string // work | companion | task | system
}

type TurnRequest struct {
    ThreadID    string
    Prompt      string
    ChannelKey  string
    SenderID    string
    TaskName    string
    Attachments []string
}

type Event struct {
    Type       EventType
    TextDelta  string
    ToolName   string
    ToolArgs   map[string]string
    ApprovalID string
    Usage      *Usage
    Error      error
}

type Usage struct {
    InputTokens     int64
    OutputTokens    int64
    ReasoningTokens int64
    TotalTokens     int64
}
```

Keep database migration small at first:

- Continue reading/writing the existing `sessions.claude_session_id` column as
  `EngineThreadID`.
- Rename in code after compatibility is proven.

## Codex App-Server Runtime

Use Codex app-server as a long-lived process controlled by the bot.

```text
codex app-server \
  --listen unix://<runtime-dir>/codex.sock
```

For websocket development or containerized deployments:

```text
codex app-server \
  --listen ws://127.0.0.1:<port> \
  --ws-auth capability-token \
  --ws-token-file <runtime-dir>/ws-token
```

The app-server process must be started with a controlled environment:

- `CODEX_HOME=<workspace-or-runtime-codex-home>`
- working directory set to the workspace/session root that should load
  `AGENTS.md`
- app-specific model/profile settings
- no inherited Claude/Codex parent-session variables that could contaminate the
  child process

## App-Server Protocol Mapping

Pin the local Codex app-server schema before implementation:

```text
codex app-server generate-json-schema --out internal/codexapp/testdata/appserver.schema.json
```

Minimum protocol surface, based on AIPM POC_R1:

- initialize client
- start or resume thread
- send turn text/items
- receive `turn/started`
- receive `item/agentMessage` text deltas
- receive approval requests
- receive command/output/tool events
- receive `turn/completed` with usage
- send approval responses
- send `turn/interrupt`
- optionally send `turn/steer`

The internal engine event normalizer should hide raw protocol naming from
session workers. Workers should only deal with:

- `delta`
- `approval_requested`
- `tool_started`
- `tool_completed`
- `turn_completed`
- `interrupted`
- `error`

## Approval Broker and Hook Replacement

This project will not rely on Codex `[hooks]` as a mandatory enforcement layer.
Borrow the AIPM S67 pattern:

- configure app-server so approval requests are emitted
- the bot receives approval requests from app-server
- policy guards decide allow/deny automatically for low-risk requests
- high-risk requests can be converted to Feishu approval cards
- if denied, bot returns the denial response to app-server
- if a turn runs away, bot calls `turn/interrupt`

Initial guards:

- max tool/turn count
- command allow/deny policy
- write/delete policy
- Feishu send target allowlist
- final output non-empty/error handling
- optional final-output moderation

## Prompt and Context

Keep old behavior:

- Write `SESSION_CONTEXT.md`.
- Inject `<system_routing>` into the prompt.
- Set `WORKSPACE_DIR=<workspaceDir>`.

Add Codex-specific compatibility:

- Ensure an `AGENTS.md` exists in each workspace.
- Prefer not to pass secrets through global Codex config.
- Consider adding a short `CODEX_WORKSPACE_CONTEXT.md` only if
  `SESSION_CONTEXT.md` is insufficient.
- Use the workspace root as app-server cwd unless a session-specific cwd is
  explicitly proven to preserve project `AGENTS.md` loading and write scope.

## Config Mapping

For v1 compatibility, keep the top-level YAML section named `claude` but parse
it as legacy engine config.

Mapping:

| Legacy field | Codex handling |
|---|---|
| `claude.timeout_minutes` | per-turn timeout / app-server turn deadline |
| `claude.max_turns` | max event/tool guard threshold |
| `claude.default_provider` | legacy provider hint; map through Codex config/profile |
| `claude.providers.*.model` | model/profile input where supported |
| `app.claude.model` | app model override |
| `app.claude.permission_mode` | maps to approval policy, not 1:1 |
| `app.claude.allowed_tools` | maps to approval broker policy |

Add a `codex:` section now, while still accepting the legacy `claude:` section:

```yaml
codex:
  engine: "app-server"
  version: "0.142.0"
  transport: "unix"        # unix | ws
  approval_policy: "untrusted"
  runtime_dir: "./runtime/codex"
  schema_path: "./internal/codexapp/testdata/appserver.schema.json"
```

## Security Posture

The app-server path should be controlled by:

- a pinned Codex config/profile
- an explicit approval policy that emits app-server approval requests
- bot-side approval decisions
- workspace-scoped cwd and writable directories
- command/file guard policies

## Package Layout

Recommended first implementation:

```text
internal/engine/      neutral request/result/interface
internal/codexapp/    Codex app-server runtime/client/event normalizer
internal/config/      legacy config plus codex config
internal/session/     change claude names to engine names gradually
internal/task/        depend on engine.Engine
internal/approval/    approval broker and policy evaluation
```

Avoid a full rename of every `claude` identifier in the first commit. Rename
public behavior after tests pass.

## AIPM-Informed Scope

Do not copy AIPM's FC/NAS topology into this bot. Reuse only the relevant
runtime ideas:

- long-lived Codex process
- thread/turn event stream
- approval requests handled by the bot
- interrupt/steer control surface
- global `CODEX_HOME` plus workspace-root `AGENTS.md` layering

Implementation sequence for this project:

1. Generate app-server schema fixture for the local target Codex version.
2. Build app-server runtime process management.
3. Implement protocol client and event normalization.
4. Wire workers to streaming turn events and Feishu card updates.
5. Implement approval broker and interrupt.
6. Add workspace compatibility init.
7. Run one work-mode and one companion-mode workspace end to end.

See `docs/05-aipm-codex-appserver-research.md`.
