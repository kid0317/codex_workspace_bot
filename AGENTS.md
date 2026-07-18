# Codex Workspace Bot 项目指南

## 沟通语言

- 除非用户明确要求其他语言，与用户沟通时优先使用中文；代码、命令、路径、协议方法名和技术标识符保持其原有形式。

## 项目背景

本仓库是对旧版本本地飞书 Workspace Bot 的 Codex 原生重写。将其视为本地 Codex 飞书 Bot/编排器，而不是通用聊天 Webhook 服务。

核心设计来源：

- `docs/01-codex-appserver-protocol-research.md`
- `docs/02-redesign-high-level.md`

修改架构、运行时、协议或存储前，必须先阅读这两份文档。

## 个人自用部署边界

- 本项目仅供其操作者个人在本机使用；不面向公开发布、商业交付、多租户服务或外部合规。
- 设计和实现应优先选择简单、可维护的本地运行方案。除非平台/核心协议要求，或用户明确提出，不要引入企业级安全架构、复杂加密方案、请求验签、防重放、密钥轮换或权限层级。
- 此边界不意味着可以将凭据提交到仓库或在日志中记录密钥，也不移除保证本地正确运行所需的最小会话绑定、幂等和输入校验。除非某项变更明确以其为目标，否则保留已有安全机制。

## AI 原生交付模式

- 本仓库是持续演进的 AI 工作环境，而不只是源代码。项目知识、可执行工作流、确定性关卡和变更后的经验应紧邻代码并纳入版本控制。
- 每个 Story 都从一份紧凑、可审查的工件开始，其中应写明：目标/背景、范围内和范围外行为、硬约束、按场景编写的验收测试、风险，以及需要人工决策的事项。不能把说明文字本身当作验收。
- 实现前先设计场景测试，并保留其意图：失败的测试可能揭示代码缺陷、过期测试或不完整需求；不得仅为通过运行而弱化或删除断言。
- 遵循交付闭环：规格对齐 -> 场景测试 -> 详细设计 -> TDD 实现 -> 独立审查 -> 集成/安全检查 -> 基于证据的发布决策 -> 复盘。
- Story 不能仅因代码合并而完成。记录支撑发布决策的证据，以及剩余风险和负责人。本地 fake 和静态 fixture 只证明其声明的本地关卡。
- 完成有意义的 Story 后，检查其 diff、测试/CI 结果、审查反馈、运行轨迹和人工纠正。将重复问题归类为缺失的知识、SOP、规则、工具、自动化关卡或过期上下文；补充最小的持久改进，并删除陈旧或重复的 Harness 内容。
- 保持 Agent 上下文精简。优先加载聚焦的代码地图、接口/文档索引和任务专属参考，而不是加载大型仓库或在 `AGENTS.md` 重复全部项目知识。
- 不要仅因存在该机制，就创建代码地图、任务专属 Skill、Hook、CI 关卡或生成的项目知识。只有实现形成稳定边界、重复工作流或可验证失败模式时才创建；并为它指定负责人、触发条件和验证路径。
- 实现开始后，以最小且有用的工件维护项目知识：稳定包关系使用代码地图，重复且需要大量判断的流程使用 Skill，可强制检查使用确定性关卡，外部能力仅使用经批准的工具集成。

## 产品方向

- 只构建 Codex 原生版本。除非用户明确改变产品方向，不要添加 Claude 兼容层、引擎抽象或多引擎回退层。
- 使用一个长期运行的 `codex app-server --stdio` 进程服务多个工作区。
- 通过 App Server 的 `thread/start` 和 `turn/start` 的 `cwd` 路由工作区；默认设计不得每个工作区启动一个 App Server。
- 飞书 App 是顶层产品应用；飞书聊天/频道是串行调度单元；Codex Thread 是一个活跃频道会话的会话后端。
- 同一频道的消息必须严格串行；不同频道可通过不同 Worker 并行处理。

## 运行时契约

- App Server 传输为基于 stdio、以换行分隔的 JSON-RPC。
- 在调用任何其他 App Server 方法前，始终先调用 `initialize`。
- 每个进程保留一个并发安全的 App Server Client。它必须拥有一个串行 stdout writer，按 JSON-RPC 请求 ID 关联每个响应，并按 `thread_id` 和 `turn_id` 路由通知与服务端请求；Worker 不得直接写入进程 stdin。
- 运行时行为优先使用这些核心方法：
  - `thread/start`
  - `thread/resume`
  - `thread/archive`
  - `turn/start`
  - `turn/interrupt`
  - `turn/steer`
  - `account/rateLimits/read`
  - `account/usage/read`
- 将返回的 Codex Thread ID 持久化到本地 session 记录。
- 每次 `thread/start`、`thread/resume` 和 `turn/start` 都传入已校验的应用 `workspace_dir`。`turn/start.cwd` 会影响后续 turn，因此绝不能依赖继承的 cwd。
- App Server 退出时，将进行中的本地 turn 标记为失败，重启进程，且不自动重放被中断的消息。后续收到消息时，尝试 `thread/resume`；失败则启动新 thread、持久化其 ID，并记录 `resume_fallback_started_new_thread`。
- `/new` 必要时中断活跃 turn，归档并清除当前本地 session，然后立即回复。下一条非命令消息才懒创建新的 Thread。
- `/cancel` 和 `/stop` 是幂等别名，必须使用 `turn/interrupt`，不得杀死 App Server 进程。`/status` 读取账户限额和用量数据；`/help` 为静态内容。

## 数据与并发

- 预期存储层是 MySQL 加进程内队列。
- 不要将 Redis、SQLite 或 PostgreSQL 作为默认持久化路径。
- Worker 队列位于内存，并以频道为作用域。
- 除非文档更新，否则保持以下设计默认值：
  - 最大活跃 Worker：20
  - 每个 Worker 队列深度：64
  - 空闲 Worker 回收：30 分钟
- 群消息的 Worker 路由键为 `group:{chat_id}:{app_id}`，p2p 消息为 `p2p:{sender_open_id}:{app_id}`。两种情况下，`chat_groups` 持久化均由 `(app_id, chat_type, chat_id)` 标识；不得用 p2p `open_id` 替代其持久化 `chat_id`。
- 不同飞书 App 可以共享同一进程，但必须按 App 配置、凭据和工作区目录隔离。已接受的个人本地 ingress 模式为 `all`：启用 App 收到的每条有效 p2p/group 文本均可处理。未来限制聊天的策略必须按 App 显式设计；不能从空列表推导出限制。
- 入队及带外控制命令处理之前，都要执行 receipt/幂等检查。队列已满或 Worker 池饱和时，返回确定性的用户可见拒绝；绝不能静默丢弃消息。

## 飞书行为

- 飞书 WebSocket 事件是主要入口路径。
- 每个飞书 App 均有自己的 receiver/client 上下文和凭据。
- 本个人本地产品当前没有 `AllowedChats` 过滤：启用 App 收到的有效 p2p/group 文本均可接受。若未来 Story 引入限制聊天规则，必须在持久化和入队前校验；空配置或未配置不得悄然改变既有 `all` 行为。
- 先支持文本；实现附件后，先将附件下载到 session 作用域的附件存储，再把文件输入传给 Codex。
- work 模式应将 App Server 增量流式更新到飞书卡片。
- companion 模式应发送纯文本分段，并使用 `[[SEND]]` 作为分段标记。
- 取消、重试、审批的卡片按钮必须路由回其所属 Worker/session，而不是全局的松散处理器。
- 发送审批卡片前持久化 pending approval。回调绑定到 app、channel、session、thread、turn、request ID 和会过期的 nonce；恰好一次地处理匹配的 App Server 请求。审批超时必须拒绝并中断 turn。

## 可观测性

- Langfuse 集成属于编排器/App Server 通知层。
- 优先直接解析 App Server 通知，而不是使用 shell hook。
- 至少保留并记录：
  - 飞书 app/channel/message 标识符
  - 本地 session ID
  - Codex thread ID
  - 活跃 turn ID
  - model/effort
  - 最终状态/错误
  - 可获得时的 token 用量事件
- 本项目当前操作者已明确裁决：个人自用的自托管 Langfuse Project 必须以**明文**记录全部业务可观测数据，包括 prompt、用户输入/输出、App Server 原始业务 payload、reasoning Item、工具参数/结果、附件/文档正文和真实业务 ID（OpenID、chat/message/document/file ID、路径、URL）。不得再引入内容加密、HMAC、ID hash、metadata-only 或内容脱敏模式。
- 唯一例外仍是运行凭据：Langfuse/飞书 Key、Authorization、Cookie、access/refresh token、数据库密码和进程环境 secret 不得进入 Langfuse、日志、仓库或飞书输出。Langfuse 仍必须 fail-open，不得阻塞 Codex/飞书业务。

## 指令加载

- Codex 全局指令来自 `CODEX_HOME/AGENTS.md`。
- 项目/工作区指令来自当前 `cwd`；本项目预期 Codex 通过已配置的回退文件名（如 `CLAUDE.md`）支持既有工作区指令文件。
- 除非具体运行时检查证明需要，否则不要在每个工作区创建桥接文件。
- 调试指令加载时，应通过 Codex 工具或 App Server 响应验证，不要根据文件名猜测。

## Go 实现规范

- 使用 `go.mod` 声明的 Go 版本；全部 Go 代码使用 `gofmt` 格式化，并保持 `go vet ./...` 无错误。
- 按设计边界组织包：`feishu`、`router`、`session`/worker、`codexapp`、`storage`、`command`、`output` 和 `observability`。避免通用 utility 包，也避免循环依赖。
- 在外部系统的消费边界定义接口（App Server、飞书、MySQL、时钟），并保持生产适配器小而专注。没有测试或替代实现的需求时，不要为本地实现细节创建接口。
- 将 `context.Context` 从入口贯穿到存储和 App Server 调用。不要在长期运行的 Worker 中保留请求 context；应派生具有明确取消和 deadline 的 Worker、turn context。
- 只有调用方需分支处理时，才返回 typed 或 sentinel error。其他情况用 `%w` 包装操作错误，并提供足以识别操作的稳定上下文，绝不包含密钥或原始用户内容。
- 为可变状态明确所有权。频道 Worker 独占其队列、活跃 session、活跃 turn 和卡片更新状态；共享注册表使用同步机制，且不得暴露可变内部状态。
- 启动时校验配置：唯一 App ID、非空工作区目录、有效 mode 和 approval policy、有边界的 Worker 设置，以及每 App 的凭据/工作区隔离。含凭据的本地配置不得纳入版本控制。
- 使用参数化 SQL 和有版本、仅向前的 MySQL migration。持久化变更必须与活跃 session 兼容，状态迁移必须是条件式且幂等。
- 写入日志、MySQL 消息记录或飞书输出前先按各自契约脱敏。Langfuse 例外遵循上方已裁决的明文业务数据策略；附件路径仍须保持 session 作用域，清理受配置 retention 限制。
- 优先使用带稳定字段的 `slog`，如 `app_id`、`channel_key`、`session_id`、`thread_id`、`turn_id`、`event` 和 `error`；不得记录密钥、授权头、原始 token 或完整消息正文。
- 保持 JSON-RPC schema/protocol 类型明确。服务端请求、通知、响应和错误是不同消息类别；服务端请求始终收到协议正确的响应，通知永不响应。

## 测试与变更关卡

- 为协议解析和请求关联、同频道串行化、队列饱和、receipt 去重、命令幂等、thread-resume 回退、审批往返/超时以及飞书卡片发送/更新失败添加聚焦测试。
- 单元和模块测试使用 fake App Server 与 fake 飞书 client。真实 App Server 和飞书冒烟测试必须是显式配置、独立的集成关卡。
- 并发代码在认为变更完成前运行目标化 race 覆盖。不得根据本地 fake 或静态证据宣称完整验收；应明确说明仍剩余的真实飞书、Codex、可观测性或运维关卡。
- 变更已接受的运行时、存储、协议、安全或并发契约时，更新两份设计文档。否则保持实现改动聚焦，不扩大产品范围。

## 当前仓库状态

本仓库当前包含 Go 服务实现、MySQL migration、运行控制脚本和面向开源的中文文档。任何状态判断仍以当前 checkout、测试、运行证据和 Story List 为准，不要只按历史复盘或旧计划描述判断。

## 本地运行时更新规则

- 任何运行时代码、本地配置、数据库 migration、初始化/导入数据或必需本地依赖变更后，重启本地服务前先应用变更并运行相关验证。要求用户验证行为前，确认新进程正在使用预期配置和数据库状态，然后报告已重启的版本与新的健康检查；绝不能要求用户针对陈旧进程验证。
- **Bot 服务的构建、启动、停止、重启和启动验证一律使用仓库根目录的 `./bot_controller.sh`。** 运行时改动验证先执行 `./bot_controller.sh build`，已有服务使用 `./bot_controller.sh restart`，未运行时使用 `./bot_controller.sh start`；需要停止时使用 `./bot_controller.sh stop`。禁止以 shell 后台命令、`nohup`、手写 `systemd-run` 或直接执行 runtime 二进制来启动本服务。该脚本负责优雅停止：Bot 会先对在途对话走 `/cancel`/`turn/interrupt` 路径，再关闭其专属 App Server；不得全局杀死其他 Codex App Server。

## SOP 索引

- [Story 从设计到 Delivered](docs/sop/story-design-to-delivery.md)：新 Story、重大整改、真实本地外部边界验收和 Delivered 后复盘时使用。
- [S01 全过程复盘](docs/archive/story-retrospectives/S01-全过程复盘-2026-07-11.md)：本 SOP 的首个真实案例、时间线和反模式证据。
- [S02 全过程复盘](docs/archive/story-retrospectives/S02-全过程复盘-2026-07-11.md)：Worker Batch、飞书 interactive card PATCH、状态机 timeout 与 Delivered 复审闭环的真实案例。
- [S03 全过程复盘](docs/archive/story-retrospectives/S03-全过程复盘-2026-07-12.md)：App Server 原始事件时间线、固定索引 schema、真实 Turn 时长边界与 L4 交付闭环。
- [S04 全过程复盘](docs/archive/story-retrospectives/S04-全过程复盘-2026-07-13.md)：CardKit 全量实体更新、companion 终态交付槽、可失败 workflow writer 与本地发布顺序。
- [S05 全过程复盘](docs/archive/story-retrospectives/S05-全过程复盘-2026-07-13.md)：附件 FIFO 输入、飞书受控能力代理、原生图片发送与历史 L4 证据的交付规则。
- [S06 全过程复盘](docs/archive/story-retrospectives/S06-全过程复盘-2026-07-14.md)：定时任务 Schema/cached catalog、Prompt 入队状态机与真实飞书 L4 的交付规则。
