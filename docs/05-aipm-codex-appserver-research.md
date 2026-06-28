# AIPM Codex App-Server Research

Date: 2026-06-28

Source project: `/root/course/ai-pm/平台开发/aipm-platform`

Primary reading set:

- `docs/story/S67_Codex沙盒系统重写.md`
- `docs/story/S67_沙盒重写设计集/沙盒系统重写_设计方案_v1.md`
- `docs/story/S67_沙盒重写设计集/POC_R1_appserver协议能力验证.md`
- `docs/story/S67_沙盒重写设计集/POC_R1_codex_appserver_schema.json`
- `docs/story/S67_沙盒重写设计集/POC_R2_codex分层加载验证.md`
- `docs/story/S67_沙盒重写设计集/沙盒三态基础Agent内容设计_v1.md`
- `docs/story/S67_沙盒重写设计集/沙盒独立测试设计与执行方案_v1.md`
- `backend/pkg/sandbox/*_v2.go`
- `backend/pkg/workspace/{nas_path,provisioner,global_codex}.go`
- `sandbox/{Dockerfile,entrypoint.sh,supervisor.py,http_adapter/adapter.py,config.toml.template}`

## Executive Summary

AIPM uses Codex as a sandbox runtime. The newest design center is Story S67,
which rewrites the sandbox around:

```text
NAS assembly -> real Codex runtime reads workspace -> orchestration-layer guards
```

The most valuable takeaway for `codex_workspace_bot` is the architectural split:

- Codex app-server is treated as an engine with a streaming protocol.
- Workspace content is mounted/read by Codex, not flattened into prompts.
- Guardrails are enforced outside the model process by the orchestrator.
- Project/user state is separated by mount topology instead of prompt policy.

AIPM's FC/NAS three-state sandbox is much heavier than `codex_workspace_bot`
needs, but its app-server protocol research, streaming model, and hook
replacement strategy are directly useful. This project will use app-server in
the current phase.

## S67 Design Index

S67 is the single index for the sandbox rewrite design set.

Core docs:

- `S67_Codex沙盒系统重写.md`: main story and latest status.
- `沙盒系统重写_设计方案_v1.md`: original consolidated design.
- `POC_R1_appserver协议能力验证.md`: app-server protocol proof.
- `POC_R2_codex分层加载验证.md`: global/project instruction loading proof.
- `沙盒三态基础Agent内容设计_v1.md`: real global `.codex` content design.
- `沙盒独立测试设计与执行方案_v1.md`: independent test harness plan.
- `S67_多视角综合Review报告.md`: multi-role review output.
- `SANDBOX_BUG_REVIEW.md`: old-system defect review.
- `skill_feishu_POC与端点映射.md`: Feishu skill endpoint mapping.

Neighbor stories:

- `S68_评测Agent引擎_GolangReAct.md`: evaluation judge engine is outside
  sandbox.
- `S69_图片生成Skill_wan2.7-image-pro.md`: image-generation skill design.

## S67 Main Architecture

S67 defines three sandbox states:

| State | Global layer | Project layer | User layer |
|---|---|---|---|
| Dev | `global/dev/.codex` read-only | dev workspace read-write | none |
| Use | `global/app/.codex` read-only | published version read-only | user dir read-write |
| Eval | `global/app/.codex` read-only | tested version read-only | local empty user dir |

Key design decisions:

- Delete the LocalLLM route after POC gates; runtime must be real Codex.
- Application behavior must come from `~/.codex` + `~/workspace`, not a hardcoded
  prompt.
- Use NAS as the only durable storage for workspace/version/user state.
- Version release is copy/rename from dev workspace to version workspace.
- Hooks are not trusted as runtime enforcement; use app-server events and
  approvals in the orchestrator.
- Use a short-lived sandbox JWT and a local proxy so the agent cannot read raw
  platform tokens.

## App-Server Protocol Findings

The local POC generated a protocol schema from Codex 0.142:

```text
codex app-server generate-json-schema --out ...
```

The POC claims the protocol exposes:

- `turn/started`
- `turn/completed`
- `item/agentMessage` deltas
- command execution output deltas
- approval requests
- `turn/interrupt`
- `turn/steer`
- `config/value/write`
- `mcpServer/reload`
- `thread/inject_items`

The current local `codex app-server --help` confirms app-server supports:

- `--listen stdio:// | unix:// | ws://IP:PORT`
- websocket auth by capability token or signed bearer token
- `generate-ts`
- `generate-json-schema`
- daemon/proxy helpers

This matters for `codex_workspace_bot`: app-server is the target runtime because
it provides streaming events and approval interception.

## Instruction Loading Findings

POC_R2 verified with `codex debug prompt-input` on Codex 0.142:

- `CODEX_HOME/AGENTS.md` loads as the global layer.
- `cwd/AGENTS.md` loads as the project layer.
- Global layer appears before project layer.
- Codex did not climb intermediate parent directories in the tested layout.

Implications:

- For app-server workspaces, always run Codex with cwd equal to the workspace
  root that contains the application `AGENTS.md`.
- Do not rely on nested `AGENTS.md` discovery unless the cwd is that nested dir.
- For compatibility workspaces, `AGENTS.md` bridge files should live at the
  workspace root.

## Guardrail Architecture

S67's strongest reusable pattern is "orchestrator-level guards":

- Pre-turn: budget/rate checks.
- During turn: stream event counting, tool-call counting, observability.
- Approval path: command/file/permission/tool approvals can be declined.
- Completion: final output can be audited before sending to Feishu.
- Interrupt: orchestrator can stop runaway turns.

S67 explicitly separates:

- Codex `[hooks]`: not trusted in headless app-server.
- `approval_policy = "untrusted"`: still required because it causes Codex to
  emit approval requests.

This distinction is directly useful for `codex_workspace_bot`. A Feishu bot
cannot rely on an invisible TUI approval prompt, so the bot process should own
approval decisions and user-facing approval cards if approvals are enabled.

## AIPM Code Shape

Current implementation is split into a new S67 path and legacy path.

New-ish path:

- `backend/pkg/sandbox/manager_iface.go`: neutral three-state interfaces.
- `backend/pkg/sandbox/manager_v2_impl.go`: `DefaultThreeStateManager`.
- `backend/pkg/sandbox/fc_runtime_v2.go`: FC runtime wrapper.
- `backend/pkg/sandbox/assembler_v2.go`: three-state mount/env assembly.
- `backend/pkg/sandbox/handler_v2.go`: HTTP/SSE surface.
- `backend/pkg/sandbox/pg_session_store_v2.go`: persistent session store.
- `backend/pkg/sandbox/guard_*`: guard chain pieces.
- `backend/pkg/workspace/nas_path.go`: NAS path builders and validation.
- `backend/pkg/workspace/provisioner.go`: dev/use/eval provisioning and publish.
- `backend/pkg/workspace/global_codex.go`: global `.codex` seed deployment.

Runtime container path:

- `sandbox/Dockerfile`: pins `ARG CODEX_VERSION=0.142.0`.
- `sandbox/entrypoint.sh`: prepares state directory and starts supervisor.
- `sandbox/supervisor.py`: manages responses_proxy, Codex, and adapter.
- `sandbox/http_adapter/adapter.py`: bridges FC HTTP to local Codex-facing
  endpoint.
- `sandbox/config.toml.template`: configures DashScope provider and
  `approval_policy = "untrusted"`.

## Implementation Status and Gaps

S67 main doc records Phase 0-4 completion:

- 43 new files.
- ~4800 LOC.
- migration `000096`.
- S67 package tests `416P / 8S / 0F`.
- dev-ci run #28 success.
- fc_regression `314 total / 227P / 0F / 87S`.

Important recorded gaps:

- ThreadID is still blank because the FC SDK currently does not return it.
- PlatformAPI endpoints are placeholders and delegated to separate stories.
- real L4 business flow is not verified yet because router v2 is not enabled in
  Dev FC by default.
- next step is enabling `SANDBOX_THREE_STATE_ENABLED=true` in Dev FC and running
  a real Feishu + Codex FC path.

Additional local-code observation:

- `sandbox/Dockerfile` pins Codex 0.142.0, consistent with S67.
- `sandbox/config.toml.template` sets `approval_policy = "untrusted"`, matching
  the approval-flow design.
- `sandbox/supervisor.py` currently builds a Codex command shaped like
  `codex --approval-mode full-auto --config ...`.
- `sandbox/http_adapter/adapter.py` currently forwards HTTP requests to
  `http://127.0.0.1:4500/v1/responses`, not visibly to the
  `thread/start -> turn/start` WS app-server protocol described in POC_R1.

That last point means `codex_workspace_bot` should treat AIPM's app-server
protocol docs as the stronger design source, but should independently verify the
exact runtime command and adapter against the local Codex CLI before copying
code.

## Lessons for codex_workspace_bot

### Use App-Server First

`codex_workspace_bot` should implement app-server as the primary engine now.
The old `cc_workspace_bot` request/response worker shape should be adapted
upward into a streaming turn model. The production path is:

- app-server process lifecycle
- thread/turn protocol client
- streaming event normalization
- approval broker
- interrupt/steer support

### Move Guardrails Above the Engine

The old `cc_workspace_bot` lets Claude/Codex decide too much inside the agent
process. AIPM's S67 suggests a better split:

- engine emits events
- bot orchestrator counts, audits, approves, interrupts
- Feishu output is sent only after the bot has the final or policy-approved
  output

For `codex_workspace_bot`, initial guards can be small:

- max turn/tool count
- message size/output size
- command approval policy
- Feishu target allowlist
- final output non-empty/error handling

### Preserve Workspace Layering

AIPM's Dev/Use/Eval model is heavier than needed, but the layering principle
maps well:

- global bot/framework layer: generated compatibility guidance and framework
  skills
- workspace layer: each assistant's `AGENTS.md`, memory, skills, tasks
- user/session layer: session state and attachments

For existing `cc_workspace_bot` workspaces, the first bridge can be:

- keep workspace as cwd
- add `AGENTS.md` if missing
- keep `CLAUDE.md` as legacy source
- keep session dirs and `SESSION_CONTEXT.md`

### Do Not Copy AIPM's FC/NAS Topology Directly

`codex_workspace_bot` is a local multi-workspace Feishu bot. It does not need:

- FC Session lifecycle
- NAS mount plans
- enterprise/scene/version routing
- eval-state clean-room semantics

But it can reuse the design ideas:

- explicit runtime lifecycle
- durable engine session ID
- per-channel serialization
- streaming event normalization
- orchestrator-controlled approvals

## Proposed Additions to codex_workspace_bot

Add the app-server engine now:

```text
internal/engine/
  streaming Engine interface
  Event, ApprovalRequest, TurnControl

internal/codexapp/
  app-server daemon/client implementation
  JSON schema pinned tests
  approval broker
  event normalizer
```

Configuration sketch:

```yaml
engine:
  type: codex-app-server

codex:
  model: gpt-5-codex
  app_server:
    listen: unix
    auth: capability-token
    approval_policy: untrusted
```

Session model:

- keep a neutral `engine_session_id`.
- in app-server mode, this maps to app-server thread ID.
- keep per-channel worker serialization as in `cc_workspace_bot`.

## App-Server Migration Checklist

Before implementing app-server mode:

1. Generate JSON schema with the local target Codex version.
2. Store schema fixture under tests.
3. Verify `--listen ws://127.0.0.1:<port>` startup.
4. Verify auth mode and token file.
5. Verify `initialize -> thread/start -> turn/start`.
6. Verify `item/agentMessage` delta parsing.
7. Verify approval requests under `approval_policy="untrusted"`.
8. Verify `turn/interrupt`.
9. Verify `turn/steer`.
10. Verify thread resume/state storage location.
11. Verify `CODEX_HOME` + cwd `AGENTS.md` loading.
12. Verify malformed legacy `.claude/skills` behavior.

## Bottom Line

For `codex_workspace_bot`, start with Codex app-server in this phase. Do not
copy AIPM's FC/NAS code; copy its POC-proven runtime boundary, event model,
approval broker idea, interrupt/steer control, and workspace-layering
discipline.
