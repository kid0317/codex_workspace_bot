# Codex Workspace Bot PRD

## 1. 产品定位

Codex Workspace Bot 是一个个人本机运行的飞书 Bot 编排器。它把飞书单聊或群聊消息转换为 Codex App Server 的 Thread/Turn 请求，并把 Codex 的可见输出、文件/文档工具结果、定时任务结果和运行状态回传给当前飞书会话。

## 2. 用户价值

- 在飞书里直接驱动本机多个项目目录中的 Codex 工作。
- 同一频道串行处理，减少多人或多消息同时修改同一 workspace 的冲突。
- 通过 MySQL、日志和可选 Langfuse 保留可复查证据。
- 支持附件输入、飞书文档读写、当前频道消息发送和本机定时任务。

## 3. 功能范围

| 能力 | 当前产品要求 |
|---|---|
| 飞书接入 | 每个 enabled App 独立 WebSocket receiver；支持 p2p/group 文本消息。 |
| App 管理 | 通过 `cmd/appctl` 在 MySQL 中创建、更新、启停、删除 App 配置。 |
| Workspace 路由 | 每个 App 绑定一个本机 `workspace_dir`、`workspace_mode`、模型和 reasoning effort。 |
| Codex 运行 | 一个长期 `codex app-server --stdio` 子进程；每次 `thread/start`、`thread/resume`、`turn/start` 都传入校验后的 cwd。 |
| 输出模式 | `work` 使用飞书卡片展示进展和正文；`companion` 成功终态后按 `[[SEND]]` 分段发送文本。 |
| 控制命令 | 支持 `/help`、`/status`、`/cancel`、`/stop`、`/new`、`/goal <目标>`。 |
| 附件和飞书工具 | 支持附件本地暂存、图片/文件发送、当前频道发消息、创建/读取飞书 docx。 |
| 定时任务 | 支持 Agent 管理当前 App/频道/用户下的 Cron Prompt 和本机 Shell Script 任务。 |
| 可观测性 | 可选接入自托管 Langfuse；业务数据明文，运行凭据剥离。 |

## 4. 非目标

- 不做公开 SaaS、多租户权限体系、管理后台或网页控制台。
- 不做 Claude 兼容、通用 Engine 抽象或多模型供应商路由。
- 不默认引入 Redis、外部消息队列、分布式 scheduler 或多实例选主。
- 不把真实 `.env`、`config.yaml`、数据库目录、日志、附件或 Codex 登录态放入仓库。
- 不自动创建飞书开放平台应用；用户需要自行在飞书开发者后台配置权限和事件订阅。

## 5. 主要用户流程

### 5.1 首次本机启动

1. 安装 Go、Docker、Docker Compose 和 Codex CLI。
2. 复制 `.env.example` 为 `.env`，复制 `config.yaml.template` 为 `config.yaml`。
3. 填写 MySQL 密码、附件/action key，按需填写 Langfuse key。
4. `docker compose up -d mysql` 启动 MySQL。
5. 使用 `go run ./cmd/appctl create ...` 写入飞书 App 配置。
6. `./bot_controller.sh build` 后 `./bot_controller.sh start` 启动服务。
7. 用 `/status` 或发送普通文本确认飞书链路。

### 5.2 日常使用

1. 用户在飞书单聊或群聊发送文本。
2. Router 做 receipt 幂等、频道归属和命令识别。
3. Worker 按频道串行合并消息并驱动 Codex Turn。
4. Bot 按模式向飞书更新卡片或发送终态文本。
5. MySQL、日志和可选 Langfuse 保存可追溯证据。

### 5.3 定时任务

1. 用户让 Agent 创建或更新 Cron 任务。
2. Agent 通过 `schedule.*` 动态工具写入当前 owner 范围内的任务。
3. Scheduler 到点后 claim run。
4. Prompt 进入同频道 Worker FIFO；Script 在 workspace 下以配置 shell 执行。
5. 非静默任务发送静态结果卡；静默任务只保留运行记录。

## 6. 验收口径

- 本机启动：`docker compose ps` 显示 MySQL healthy，`/healthz` 返回 `ok`，`/readyz` 不出现 receiver failed。
- 单元/模块门槛：`go test ./... -count=1`、`go vet ./...`、`go build ./cmd/server ./cmd/appctl`。
- 并发敏感变更：对相关包运行 `go test -race`。
- 外部链路验收：真实飞书 p2p/group、真实 Codex App Server、可选 Langfuse read-back 必须单独说明，不能用 fake 测试替代。

## 7. Story 状态入口

所有 Story 的最新状态以 [Story List](story/STORY_LIST.md) 为入口。状态含义：

- `Delivered`：代码、文档、测试和真实边界验收均已闭环。
- `已实现，待审计`：主要代码存在，但还缺最终 Delivered 审计或文档状态收口。
- `In Development`：已有实现或验证进展，但仍缺明确 P0 验收项。
- `Draft / 待实现`：设计存在，代码未完成或尚未进入实现。
