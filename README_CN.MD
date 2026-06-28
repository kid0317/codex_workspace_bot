# Codex Workspace Bot

Codex Workspace Bot 是 `cc_workspace_bot` 的 Codex 原生后继版本。

第一阶段兼容目标是直接读取现有 `cc_workspace_bot` 风格的 app 列表和 workspace 目录，同时为后续 Codex app-server 引擎替换准备稳定边界。

项目文档位于 `docs/`。

## 当前状态

Story 06 提供第一版可运行 Go scaffold：mock 引擎边界、旧配置/workspace/DB 兼容、按频道串行 worker、会话/任务/附件/审批编排、guardrail、debug 模拟 API，以及 fixture 驱动测试。

主要文档：

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

## 兼容原则

不要要求现有 workspace 在首次运行前整体重写。新 runtime 应读取现有配置形状，在必要时初始化 Codex 侧兼容文件，并保留 memory、tasks、sessions、attachments、飞书路由等 workspace 数据。

## 启动

```bash
cp config.yaml.template config.yaml
./build.sh
./start.sh start
```

本地 debug 测试需要显式启用 token：

```bash
DEBUG=true DEBUG_TOKEN=dev-token ./start.sh restart
```

进程命令：

```bash
./start.sh start
./start.sh stop
./start.sh restart
./start.sh status
```

`start.sh` 会启动 `dist/codex_workspace_bot`，并管理 `server.pid`、`server.log`、`server.log.wf`。

第一版 scaffold 使用 `engine.type: mock`。真实飞书网络调用和真实 Codex app-server 协议客户端仍在包边界后，属于后续 story。

## 构建

使用仓库内构建脚本完成本地校验并输出二进制：

```bash
./build.sh
```

脚本会执行 `gofmt -l .`、`go test ./...`、`go vet ./...`，然后构建：

```bash
dist/codex_workspace_bot
```

可选覆盖：

```bash
OUT_DIR=/tmp/codex-build BINARY_NAME=bot ./build.sh
```

## Debug 安全

Debug API 只用于本地开发和测试。

- 非 dev/test 环境保持 `server.debug_enabled: false`，除非有明确批准的测试窗口。
- 正常使用保持 `server.debug_bind: 127.0.0.1`。
- 启用 debug 时设置 `server.debug_token`，请求使用 `X-Debug-Token` 或 `Authorization: Bearer ...`。
- 不要把 debug endpoint 暴露到公网 ingress。
- debug 附件 `temp_dir` 应位于框架自有 download/temp 目录下，不要指向整个 workspace root。

## Dev/Test 部署检查

dev 和 test 使用同一二进制与配置模板，只替换环境相关 workspace 路径和脱敏凭据。

可重复的本地 mock scaffold 证据：

```bash
bash scripts/story06_smoke.sh
```

脚本会在 `docs/evidence/story06/latest/` 记录 `/health`、`/debug/dispatch`、`/debug/task/run`、SQLite 证据、artifact cleanliness、debug disabled 检查。

1. 构建：`./build.sh`
2. 启动：`DEBUG=true DEBUG_TOKEN=dev-token ./start.sh restart`
3. 健康检查：`curl -s http://127.0.0.1:8080/health`
4. Dispatch smoke：

```bash
curl -s -X POST http://127.0.0.1:8080/debug/dispatch \
  -H 'Content-Type: application/json' \
  -H 'X-Debug-Token: dev-token' \
  -d '{"app_id":"demo-assistant","chat_id":"oc_demo","sender_id":"ou_demo","message_id":"manual-1","text":"hello"}'
```

5. Task smoke：

```bash
curl -s -X POST http://127.0.0.1:8080/debug/task/run \
  -H 'Content-Type: application/json' \
  -H 'X-Debug-Token: dev-token' \
  -d '{"app_id":"demo-assistant","task":{"id":"demo-assistant/manual","target_type":"p2p","target_id":"oc_demo","send_output":true,"prompt":"manual task","enabled":true}}'
```

6. DB 证据：

```bash
sqlite3 workspaces/demo-assistant/bot.db 'select role, content from messages order by created_at;'
sqlite3 workspaces/demo-assistant/bot.db 'select status, error_kind from turns order by created_at;'
```

## 回滚

该 scaffold 使用 additive SQLite migration，并把 runtime 状态写入每个 app workspace。

- 先停止进程，让 channel worker drain。
- 回滚上一版 binary/config。
- 不要删除或重写 `bot.db`；保留它用于 additive forward migration。
- 如果 debug 测试污染了 workspace，只归档生成的 `sessions/` 子目录，或从环境快照恢复 workspace。

## 监控

runtime 输出结构化 lifecycle 事件，包含 app、channel、session、turn、task、engine thread ID。至少采集以下日志：

- `dispatch_rejected` / `queue_overflow`
- `turn_failed`
- `approval_requested`
- `approval_resolved`
- `attachment_expired`
- 进程 shutdown 耗时

## 数据留存与隐私

Story 06 是本地 mock scaffold。它会在每个 app workspace 存储本地开发/测试数据：

- `bot.db`：channels、sessions、messages、tasks、attachment metadata、approvals、turns。
- `sessions/`：生成的 `SESSION_CONTEXT.md` 和已消费附件副本。
- `tasks/`：YAML 任务定义。
- `tmp/attachments` 或配置的 `attachments.temp_dir`：临时下载/mock 附件文件。

不要使用真实飞书凭据或生产用户数据运行该 scaffold。Debug API 只能在受控本地/dev-test 场景下配合 token 启用。runtime cleanup 会按 retention 清理 pending attachments；message、task、turn、approval 历史会保留在 `bot.db`，直到 workspace owner 归档或删除 workspace 副本。

dev/test 数据删除流程：

1. 停止进程，让 worker 和 scheduler drain。
2. 归档或删除测试环境复制出来的 workspace 目录。
3. 如果保留 workspace，只在确认不再需要审计证据后删除生成的 `sessions/`、`tmp/attachments` 和复制出的 `bot.db`。
4. 不要原地删除类似生产的旧 `bot.db`；先做 snapshot，只使用 additive migration。

第三方依赖说明见 `THIRD_PARTY_NOTICES.md`。

## 验证

自动化检查：

```bash
go test ./...
go test -race ./internal/session ./internal/task ./internal/approval ./internal/db
go build ./...
```

Story 06 交付建议采集的手工证据：

- debug-enabled server 的 `/health` 响应。
- work-mode message 的 `/debug/dispatch` 响应。
- dispatch 后 SQLite 查询显示 user 和 assistant messages。
- SQLite 查询显示 work-mode thread reuse，并在 `/new` 后生成新 thread。
- attachment-only dispatch 后再发 text，证明 pending 到 consumed 状态。
- system task run 生成 `sessions/_system/{slug}/SESSION_CONTEXT.md`。
- workspace init 对受保护文件的 hash 对比。
- debug dispatch 后的 `git status --ignored --short` 输出。
- 非 dev 配置下 debug disabled 检查。

## 旧测试对齐

初版 scaffold parity 状态：

| 旧能力区域 | 新状态 |
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
