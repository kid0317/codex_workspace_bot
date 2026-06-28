# Codex Workspace Bot

Codex Workspace Bot is the Codex-native successor to `cc_workspace_bot`.

The first compatibility target is direct operation of an existing legacy
`cc_workspace_bot`-style app list and workspace directories, while preparing
the core Claude Code engine boundary for Codex app-server.

Project documentation lives under `docs/`.

## Current Status

Story 06 provides the first runnable Go scaffold: mock-backed engine boundary,
legacy config/workspace/DB compatibility, per-channel workers, session/task/
attachment/approval orchestration, guardrails, debug simulation APIs, and
fixture-backed tests.

Primary docs:

- `docs/00-cc-workspace-bot-research.md`
- `docs/01-codex-engine-migration-plan.md`
- `docs/02-workspace-compatibility-plan.md`
- `docs/03-implementation-roadmap.md`
- `docs/04-decisions.md`
- `docs/05-aipm-codex-appserver-research.md`
- `docs/06-framework-scaffold-story-design.md`
- `docs/07-framework-scaffold-design-review.md`
- `docs/08-framework-scaffold-design-review-v2.md`
- `docs/09-framework-scaffold-test-design.md`
- `docs/10-framework-scaffold-test-design-review.md`

## Compatibility Principle

Do not require all existing workspaces to be rewritten before the bot can run.
The new runtime should read the existing config shape, initialize Codex-side
compatibility files where needed, and preserve workspace data such as memory,
tasks, sessions, attachments, and Feishu routing behavior.

## Run

```bash
cp config.yaml.template config.yaml
./start.sh
```

For local debug testing, start with an explicit token:

```bash
DEBUG=true ./start.sh
```

The first scaffold uses `engine.type: mock`. Real Feishu network calls and the
real Codex app-server protocol client remain behind package boundaries for
follow-up stories.

## Build

Use the checked-in build script for local verification and binary output:

```bash
./build.sh
```

The script runs `gofmt -l .`, `go test ./...`, `go vet ./...`, and then builds:

```bash
dist/codex_workspace_bot
```

Optional overrides:

```bash
OUT_DIR=/tmp/codex-build BINARY_NAME=bot ./build.sh
```

## Debug Safety

Debug APIs are intended for local development and test only.

- Keep `server.debug_enabled: false` outside dev/test unless a test window is explicitly approved.
- Keep `server.debug_bind: 127.0.0.1` for normal use.
- When debug is enabled, set `server.debug_token` and send it as `X-Debug-Token` or `Authorization: Bearer ...`.
- Do not expose debug endpoints through a public ingress.
- Keep debug attachment `temp_dir` under a framework-owned download/temp directory. Do not point it at a whole workspace root.

## Dev/Test Deployment Check

Use the same binary and config template in dev and test, with environment-specific workspace paths and redacted credentials.

For repeatable local mock-scaffold evidence, run:

```bash
bash scripts/story06_smoke.sh
```

The script records `/health`, `/debug/dispatch`, `/debug/task/run`, SQLite evidence, artifact cleanliness, and debug disabled checks under `docs/evidence/story06/latest/`.

1. Build: `go build ./...`
2. Start: `DEBUG=true DEBUG_TOKEN=dev-token ./start.sh`
3. Health: `curl -s http://127.0.0.1:8080/health`
4. Dispatch smoke:

```bash
curl -s -X POST http://127.0.0.1:8080/debug/dispatch \
  -H 'Content-Type: application/json' \
  -H 'X-Debug-Token: dev-token' \
  -d '{"app_id":"demo-assistant","chat_id":"oc_demo","sender_id":"ou_demo","message_id":"manual-1","text":"hello"}'
```

5. Task smoke:

```bash
curl -s -X POST http://127.0.0.1:8080/debug/task/run \
  -H 'Content-Type: application/json' \
  -H 'X-Debug-Token: dev-token' \
  -d '{"app_id":"demo-assistant","task":{"id":"demo-assistant/manual","target_type":"p2p","target_id":"oc_demo","send_output":true,"prompt":"manual task","enabled":true}}'
```

6. DB evidence:

```bash
sqlite3 workspaces/demo-assistant/bot.db 'select role, content from messages order by created_at;'
sqlite3 workspaces/demo-assistant/bot.db 'select status, error_kind from turns order by created_at;'
```

## Rollback

This scaffold uses additive SQLite migrations and writes runtime state under each app workspace.

- Stop the process first so channel workers can drain.
- Roll back by redeploying the previous binary/config pair.
- Do not delete or rewrite `bot.db`; preserve it for additive forward migrations.
- If a debug test polluted a workspace, archive only the generated `sessions/` subdirectory or restore the workspace from the environment snapshot.

## Monitoring

The runtime emits structured lifecycle events with app, channel, session, turn, task, and engine thread IDs. At minimum, collect logs for:

- `dispatch_rejected` / `queue_overflow`
- `turn_failed`
- `approval_requested`
- `approval_resolved`
- `attachment_expired`
- process shutdown duration

## Data Retention And Privacy

Story 06 is a local mock scaffold. It stores local development/test data in each app workspace:

- `bot.db`: channels, sessions, messages, tasks, attachment metadata, approvals, and turns.
- `sessions/`: generated `SESSION_CONTEXT.md` files and consumed attachment copies.
- `tasks/`: YAML task definitions.
- `tmp/attachments` or configured `attachments.temp_dir`: temporary downloaded/mock attachment files.

Do not use live Feishu credentials or production user data with this scaffold. Debug APIs should only be enabled for controlled local/dev-test runs with a token. Runtime cleanup expires pending attachments according to configured retention; message, task, turn, and approval history remains in `bot.db` until the workspace owner archives or removes the workspace copy.

Deletion procedure for dev/test data:

1. Stop the process so workers and schedulers drain.
2. Archive or remove the copied workspace directory for the test environment.
3. If retaining the workspace, remove only generated `sessions/`, `tmp/attachments`, and the copied `bot.db` after confirming the data is not needed for audit evidence.
4. Never delete a production-like legacy `bot.db` in place; take a snapshot and use additive migrations only.

Third-party dependency notes are tracked in `THIRD_PARTY_NOTICES.md`.

## Verification

Automated checks:

```bash
go test ./...
go test -race ./internal/session ./internal/task ./internal/approval ./internal/db
go build ./...
```

Manual evidence to collect for Story 06 delivery:

- `/health` response from a debug-enabled server.
- `/debug/dispatch` response for a work-mode message.
- SQLite query showing user and assistant messages after dispatch.
- SQLite query showing work-mode thread reuse, then a new thread after `/new`.
- Attachment-only dispatch followed by text dispatch showing pending to consumed state.
- System task run showing `sessions/_system/{slug}/SESSION_CONTEXT.md`.
- Workspace init hash comparison for protected files.
- `git status --ignored --short` output after a debug dispatch.
- debug disabled check for non-dev configs.

## Old Test Parity

Initial scaffold parity status:

| Old area | New status |
|---|---|
| config defaults and legacy shape | ported |
| workspace init and protected files | ported |
| DB open/migrate and `claude_session_id` mapping | ported |
| Feishu channel/reply/rich text parsing | ported |
| output `[[SEND]]` segmentation | translated |
| engine execution | translated to neutral mock engine contract |
| session thread reuse and `/new` | ported |
| task YAML defaults and system task run | ported |
| approval state machine | translated |
| app-server runtime process | deferred to follow-up story |
