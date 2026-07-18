# S06：定时任务与 Agent 工具

> **状态**：Delivered（2026-07-14；v4 `list_own → update(id,version)`、修复后的 Prompt 与 Script 均完成真实飞书 L4）  
> **关联概设**：[HLD v4.4](../02-redesign-high-level.md) §3、§5、§8、§9、§10、§12  
> **依赖**：S01–S05 已 Delivered；本机 MySQL；已登录的本机 Codex App Server；测试飞书 App 的消息与 CardKit 权限。  
> **范围更正**：此前同编号的“飞书斜杠命令与时间上下文”草稿不再是 S06 的规范范围，保留为历史 `Superseded` 文档；本文件是此后唯一的 S06 设计。

## 1. 一句话目标

让已绑定到某个飞书 App、频道和用户的 Codex Agent 能列出、创建和修改自己的 CronTab 定时任务；任务到点后由本服务以 bot 当前 OS 用户直接执行保存的本机 shell command，或像该频道收到一条消息一样进入原 Channel Worker FIFO 运行 Prompt，并依照静默设置交付或抑制自动结果卡片。

## 2. 背景

现有 S01–S05 已有 MySQL 持久化、按 `group:{chat_id}:{app_id}` / `p2p:{sender_open_id}:{app_id}` 串行的 Worker、bot-owned 单一 Codex App Server、动态工具 `item/tool/call` 路由，以及 work 模式的流式卡片。它没有能由 Agent 管理的任务实体，也没有服务重启后可恢复、可审计的 Cron 执行边界。

HLD 旧的“workspace YAML → fsnotify → gocron”仅是未实现的 P1 占位，且不能表达任务归属、动态工具调用的幂等性、MySQL 事务、脚本安全边界或 Prompt 与 Worker 的串行关系。本 Story 以 MySQL 为唯一真相源；文件监控、YAML 任务源、Redis、SQLite 和每任务独立 App Server 都不进入本期。

本机 2026-07-12 生成的 App Server schema 已由 S05 验证：`thread/start.dynamicTools` 可注册 namespace 工具，`item/tool/call` 必须以当前 `threadId + turnId + callId` 精确绑定并回一次 protocol-correct result。S06 因此把定时任务做成 bot 的受控 `schedule` dynamic-tool namespace，不把数据库凭据或调度 CLI 交给 Agent。

## 3. 范围

### 3.1 本期做什么

1. 将 Thread dynamic-tool catalog 升级为 `s06-schedule-v4`：注册完整的既有 `feishu` 加 `schedule` namespace（`list_own`、`create`、`update`）。v2 将 Script 参数从 `script_id` 改为直接 `command`；v3 使 `schedule.update` 可选接收 `list_own` 返回的 `kind`，但必须与原任务类型一致，不能改变类型；v4 将 update 的任务标识统一为 list 返回的 `id`（不再另名为 `task_id`）。三者均是强制 catalog 失效版本；升级状态、旧/新 Thread pointer 和目标 catalog 必须持久化在 ChatGroup，任何崩溃恢复均不得 resume 旧 catalog。Tool handler 只能从当前 exact attempt 的可信 ActorPrincipal 推导 App、频道、ChatGroup 与创建者；模型不能传或覆盖这些身份字段。
2. 接受标准 **五字段** CronTab（分钟、小时、日、月、星期；无秒字段），固定使用 `Asia/Shanghai` 解释并持久化下一次到点时间；创建和更新均在写库前解析、规范化并计算下一次运行。
3. 增加 MySQL `scheduled_tasks`（任务定义）、`scheduled_task_runs`（每次执行）、`scheduled_task_deliveries`（自动交付 outbox）和 `scheduled_task_tool_calls`（动态工具幂等回放）四张活跃业务表，并用 forward-only migration 建立外键、唯一键、领取索引和状态约束。既有 `scheduled_script_definitions` 仅为历史兼容表，新任务不读写它。
4. 服务启动后运行一个进程内 scheduler：MySQL `next_run_at` 是权威，到点后先原子 claim 一次 run，再把任务执行权交给本进程；启动/周期扫描会恢复未完成 lease，但绝不重放已经未知是否产生外部副作用的 run。
5. `prompt` 任务到点后构造同一 App、同一 ChatGroup、同一 owner reply target 的 synthetic Message，插入既有 Worker FIFO；它保留正常的 Thread resume/start、动态工具、超时、同频道串行和 App Server 事件处理。
6. `script` 任务保存 Agent 传入的完整 `command`；到点后 executor 在任务 App workspace 中以 bot 当前 OS 用户通过配置 shell 的 `-c` 直接执行，捕获 stdout/stderr、退出状态、超时和输出截断。没有管理员登记、`script_id`、降权或网络隔离。脚本不是普通模型 Turn，绝不经 App Server。
7. `silent=true` 时，Prompt 任务不创建/更新自动结果卡，脚本任务不发送自动结果；`silent=false` 时，二者均以当前飞书 App 的静态结果卡交付（必要时只做一次 plain-text fallback）。Prompt 仍保留 Worker 所有权与其既有超时/最终状态；静默不撤销模型在 Prompt 内显式发起的、已获 S05 授权的当前频道工具动作。
8. 为 `list_own` 提供 owner-bound cursor pagination，设置 per-owner/per-App enabled task 配额、每 tick Prompt dispatch 预算和 Script 并发上限；饱和时确定性结束当前 run，不建立无界内存队列。
9. 覆盖 Cron 校验、归属/多发送者 batch、catalog upgrade/resume、动态工具回放、claim/lease/restart、同频道 FIFO/控制清队、静默、脚本安全/输出截断与卡片失败的 L1/L2 测试，并定义 L3/L4 本地真实集成校验。

### 3.2 本期不做什么

- 不提供 delete 工具、自然语言时间解析、一次性延迟任务、秒级 Cron、时区选择、日历/节假日表达式、任务依赖 DAG、批量导入或 YAML/fsnotify 热加载；用户可用 `update {enabled:false}` 停用任务。
- Script 的 `command` 是用户请求的完整本机 shell 命令；Agent 不得凭空添加命令、参数、环境变量、工作目录或交付要求。任务 App workspace 是固定执行目录，身份、App ID、chat ID、open ID、凭据和 reply target 仍由可信 route 推导而非模型参数。
- 不保证服务停机期间补跑所有历史 slot：超过运行宽限的 slot 被记录为 `skipped_misfire`，只计算下一次未来时间；不因为重启重复执行 Prompt、Shell 或飞书发送。
- 不实现分布式 scheduler、跨进程主从选举、Redis queue、外部消息队列、任务 DAG、管理员代管/查看他人任务，或跨群/跨 App 通知。
- 不改变 S04 companion 的普通对话交付语义；本 Story 对非静默计划任务一律使用独立静态结果卡，避免把自动任务混进 companion 的 `[[SEND]]` 分段会话。
- 不让任务工具读取/暴露他人的 Prompt、脚本路径、输出、任务 ID 或运行记录；Prompt、脚本路径和 console 永不进入 MySQL、日志、Langfuse 或 workflow JSONL。**唯一例外**是 non-silent Script 的结果卡：它向任务所属频道展示经过 UTF-8 安全转换、Markdown 转义、大小限制的 console 内容，以满足本 Story 的用户可见需求。

## 4. 依赖与前置条件

| 依赖 | 状态 | S06 使用方式 |
|---|---|---|
| S01 MySQL、App/ChatGroup、receipt 与 CardKit sender | 已 Delivered | 任务归属使用 `apps`/`chat_groups`；非静默结果由同 App Sender 发送。 |
| S02 Worker FIFO 与 worker key | 已 Delivered | Prompt run 必须通过同一频道 Worker 排队，不可直接调 Processor；S06 新增 ActorPrincipal merge boundary 与 ScheduledPrompt profile。 |
| S03 App Server runtime | 已 Delivered | Prompt run 复用一个 bot-owned stdio child 和 existing Thread resume/start。 |
| S04 输出与终态控制 | 已 Delivered | 正常 Prompt Turn 的终态/取消语义保持；S06 的 task-result card 是独立静态消息。 |
| S05 dynamic tool router 与 encrypted action result keyring | 已 Delivered | `schedule` 工具复用 exact attempt binding、单 writer response、call-id 回放模式；新增自身 payload/keyring，不借用 Feishu action 记录。 |
| `robfig/cron/v3` | 新依赖，实施前锁定版本 | 仅使用其五字段 parser 与 `Schedule.Next` 计算；不是另一个持久化真相源。 |
| `Asia/Shanghai` IANA timezone data | Go 运行时前置 | 服务启动时必须加载成功；不得回退为 host local timezone。 |
| `schedule.payload_keys` / `schedule.owner_hmac_keys` | 现有兼容配置 | 任务和 tool replay 已按个人本机部署选择明文保存；owner HMAC 仅用于 owner 查询、cursor 与 arguments replay。 |
| Direct Script executor | 本地前置 | `scripts.enabled`、绝对可执行 `shell_path`、timeout/output/concurrency 配置通过启动校验；不需要管理员、registry、低权限 runner 或网络隔离。 |

## 5. 核心设计决策

| 问题 | 结论 | 原因 | 后果 |
|---|---|---|---|
| 身份与“自己的任务” | owner 是 `(app_id, chat_group_id, creator_open_id)`；个人本机部署将 `creator_open_id` 明文保存，并保留 `creator_open_id_hmac` 用于 owner 查询与 cursor/replay 关联。Ingress 将不可日志的 `ActorPrincipal{OpenID}` 放入 Message；Worker 只合并同一 Actor 的连续普通消息，随后把它 exact-bind 到 attempt/ToolHandler。 | App、频道和用户是需求明确的归属维度；模型参数不可被信任，现有跨用户合并 Batch 也不能替人猜 owner。 | 同用户不同频道、同群不同机器人或不同用户均互不可见、不可改。缺 actor 或混合 actor Batch 的 schedule call 返回 `schedule_owner_ambiguous`。 |
| 任务类型 | `kind=prompt|script`；Prompt 存未来 Agent 的独立用户指令，Script 存用户要求执行的完整 `command`。 | 两种执行边界不同，但这是个人本机产品，Script 允许直接 command。 | script executor 不使用 App Server；Prompt 不能绕过 Worker。 |
| Cron 语义 | 用 `robfig/cron/v3` 的 standard parser 解析五字段，固定 `Asia/Shanghai`，额外拒绝秒字段、宏、`TZ=`/`CRON_TZ=`、空表达式；`next_run_at` 存 UTC `DATETIME(3)`。 | 固定、可测试且与中国本地飞书使用一致；UTC 存储避免主机时区变化或表达式绕过时区。 | `Schedule.Next(t)` 严格大于 `t`；每个 list result 展示 cron 与固定时区，不额外支持用户选时区。 |
| 定时触发 | `scheduled_tasks.next_run_at` 是唯一调度真相；ticker 只发现 due task，run claim 才授权副作用。 | 进程内 cron callback 不是崩溃恢复证据，MySQL 必须能解释为何执行/跳过。 | scheduler 可重建、可在启动恢复；未来多实例也不会无条件双跑。 |
| 停机/迟到 | `now - next_run_at > misfire_grace`（默认 60 秒）时写一个 `skipped_misfire` run，并以 `Schedule.Next(now)` 推进到第一个未来 slot；其余 due slot 执行一次且以 `Schedule.Next(scheduled_for)` 推进。 | 不能在恢复时静默执行过时 Prompt/脚本，也不能把短暂调度抖动当漏跑。 | 不补跑历史队列；指标/列表可见最近 skip，且 `Next()` 的严格大于语义被固定测试。 |
| 并发与领取 | 先插入唯一 `(task_id, scheduled_for)` run，再以短 `claimed` lease 条件领取；同一 transaction 把明文 payload 固化到 run。成功入 Worker 后是不可重放 `queued`，Worker 开始时才取得/心跳 `running` lease，执行完成只允许 claim token 条件收尾。 | FIFO 等待可超过 Turn timeout，不能让入队 lease 在尚未运行时失效或被第二执行者夺取，也不能让后续 task update 改写已经领取的语义。 | run `state` 只允许 `claimed → queued → running → succeeded|failed|cancelled|unknown` 或 `skipped`；细分原因是 `error_code`。任何 token/lease 失效者不得继续执行或交付。 |
| Prompt 的排队位置 | claim 后创建 synthetic Message 并 `worker.Manager.Accept`；`scheduled_task_run_id` 使其成为独占 Batch 边界，不与普通文本 merge。 | 用户明确要求模拟原群并让 Worker 自然消费；合并会破坏 run/result 对应关系。 | 同频道按 FIFO；队满/池饱和把 run 标 `failed_enqueue`，任务下次周期不受阻。 |
| Prompt 线程与权限 | Prompt 复用该 ChatGroup 当前 Codex Thread、App workspace/model/effort 和 dynamic tools；source user/reply target 是任务 owner。 | 保持对话上下文和 S05 当前频道约束，不新开每任务 App Server/Thread。 | 多人群中的计划任务以创建者身份进入 route；任务本身不获得跨频道权限。 |
| 静默 | 静默抑制 orchestrator 自动卡/文本；不改变 Prompt 里模型显式调用 S05 `feishu.*` 的已授权副作用。 | “不推返回卡”不等于撤销模型明确任务动作，且该动作已有独立 ledger。 | 任务 list 明示 `silent`；未来若需要“无任何外部副作用”另开授权 Story。 |
| Shell 执行 | Script task 持有完整 `command`，executor 用 `shell_path -c command` 在任务 App workspace 直接运行。 | 用户已明确这是个人本机产品，不设管理员或隔离机制。 | command、退出码和有限 stdout/stderr 可由 owner 的 task/run 查验；timeout/cancel 杀整个进程组并 wait/reap。 |
| 数据保护 | 任务 payload、run payload 和 tool replay result 以明文保存，符合本机个人部署要求；普通日志、Langfuse 与 delivery 元数据仍不得写 command、Prompt、输出或凭据。 | 用户需要能直接检查本机 MySQL。 | owner HMAC 继续仅服务 owner 查询、cursor 和 replay 关联。 |
| 工具幂等 | `scheduled_task_tool_calls` 以 `(app_id, thread_id, turn_id, call_id)` 唯一、arguments HMAC 和明文 result 做 at-most-once 回放；claim、task mutation/version 和 terminal tool result 在**同一 MySQL transaction**提交。 | App Server 可重发 server request；create 不能重复建任务，update 不能重复改版本，也不能留永久 in-flight。 | 同 callId 不同参数拒绝；in-flight 有短 lease，重启 reconciliation 安全转 rejected；同 digest 回放相同 result。 |
| 更新冲突 | `update` 必须带 `list_own` 返回的 `id` 与当前 `version`；SQL 条件更新 `version=expected` 后自增。 | 防止同一 owner 的并发 Turn 覆盖静默、payload 或 cron，同时避免模型在复用列表对象时重新命名任务标识。 | mismatch 返回当前 metadata（不含 payload）并要求 Agent 先 list；禁用更新会原子撤销 future `next_run_at`，但不撤销已 claim/queued/running 的不可变 run 快照。 |
| 输出与可观测性 | 非静默任务只创建一个终态 static result-card intent；每次 card/text 都是 `scheduled_task_deliveries` 中独立的 `pending → in_flight → sent|rejected|unknown|suppressed` intent。仅 primary card 明确 `rejected` 时，才创建一次独立 fallback-text intent；任一 `unknown` 永不重发。 | 自动任务没有原始用户消息卡可安全复用；必须避免交付竞争和 crash 后重复可见消息。 | run 结果绝不驱动/修改普通 Batch 的活动 S04 card；Prompt 正文/console 不落日志，non-silent Script console 仅进入结果卡。 |
| 路由撤销与容量 | due claim 必须重校验 `apps.enabled`、ChatGroup `schedule_enabled` 和 per-owner/per-App quota；app disable、bot 离群/频道撤销事件原子关闭其 task route。 | silent Script 也不能在 App/频道已经失效后继续执行；Cron 的最小粒度仍可造成每分钟洪峰。 | 失效 route 写 `failed_route_revoked`；默认 owner 100、App 1,000 enabled task，Prompt 每 tick 最多 20，Script 全局最多 2 并发，超限写 `failed_capacity`。 |

## 6. 主链路与数据/接口契约

### 6.1 表与状态契约

`005_s06_scheduled_tasks.sql` 必须是 forward-only、checksum-protected migration；所有业务 SQL 参数化。数据库启用 `utf8mb4`，时间全部存 UTC。run `state`、`error_code` 与 delivery row `stage/outcome` 是独立字段：state 只描述执行生命期，细分失败只写 error code，交付 intent 单独描述外部可见性。

#### `chat_groups` catalog upgrade fields

同一 migration 为既有 `chat_groups` 增加 `codex_tool_catalog_version VARCHAR(64) NOT NULL DEFAULT 's05-feishu-v2'`、`catalog_upgrade_state ENUM('stable','archive_pending','start_pending') NOT NULL DEFAULT 'stable'`、`catalog_upgrade_from_thread_id VARCHAR(128) NULL` 和 `catalog_upgrade_target VARCHAR(64) NULL`；`schedule_enabled BOOLEAN NOT NULL DEFAULT TRUE` 也是本次 route revoke 的持久开关。所有字段与 `codex_thread_id` 在同一 `SELECT ... FOR UPDATE` 更新。

空闲 Worker 发现 catalog 旧于目标版本时，先将 `(old_thread_id, target='s06-schedule-v4', archive_pending)` 持久化；随后对 `catalog_upgrade_from_thread_id` 调用可重试的 `thread/archive`，并以旧 pointer + `archive_pending` 为条件 CAS 清空 `codex_thread_id`、转 `start_pending`。仅在 `start_pending` 时 `thread/start` 传全 catalog；收到新 ID 后以状态/空 pointer CAS 写入 `codex_thread_id`、`codex_tool_catalog_version` 并转 `stable`。重启时：`archive_pending` 重试 archive，`start_pending` 只新建目标 catalog Thread；若 `thread/start` 已返回但进程在其 ID 持久化前崩溃，该 Thread 是不可恢复 orphan，绝不 resume，下一次 start 新建一个可持久化的 Thread。任何 `stable` 行的 resume 都必须先检查其持久版本等于目标，否则先走本流程；因此不存在“按旧 ID resume 后补 tools”的路径。

#### `scheduled_tasks`

| 字段 | 约束与用途 |
|---|---|
| `id CHAR(36)` | task UUID。 |
| `app_id`, `chat_group_id`, `creator_open_id`, `creator_open_id_hmac` | 非空外键/归属。`creator_open_id` 是个人本机部署的明文 route 数据；HMAC 支持 owner 精确查询、cursor 与 replay 关联。旧 `creator_open_id_enc/creator_key_version` 仅保留迁移兼容。 |
| `kind VARCHAR(16)` | 仅 `prompt` 或 `script`。 |
| `cron_expression VARCHAR(128)`, `timezone VARCHAR(64)` | canonical five-field Cron 与恒定 `Asia/Shanghai`。 |
| `payload_text TEXT` | 明文 Prompt 或 Script `command`；个人本机部署可直接检查。历史 `payload_enc/payload_key_version/payload_hmac/payload_bytes` 为迁移兼容列，新写入为空。 |
| `silent BOOLEAN`, `enabled BOOLEAN`, `version BIGINT UNSIGNED` | 交付策略、停用和 optimistic concurrency。 |
| `next_run_at DATETIME(3) NULL`, `last_run_at DATETIME(3) NULL` | scheduler 唯一读取的未来 slot 与最近处理时刻。停用时 `next_run_at=NULL`。 |
| `created_at`, `updated_at` | 审计时间。 |

约束：`FOREIGN KEY (app_id) REFERENCES apps(id)`、`FOREIGN KEY (chat_group_id) REFERENCES chat_groups(id)`；`KEY idx_scheduled_tasks_due(enabled,next_run_at)`、`KEY idx_scheduled_tasks_owner(app_id,chat_group_id,creator_open_id_hmac,updated_at)`。创建不强制 name；Agent 以 task ID 和简短安全摘要管理任务，避免名称成为第二个隐私/冲突契约。

#### `scheduled_task_runs`

| 字段 | 约束与用途 |
|---|---|
| `id CHAR(36)`, `task_id CHAR(36)`, `scheduled_for DATETIME(3)` | run ID、task 外键和时间 slot；`UNIQUE(task_id,scheduled_for)` 是防重边界。 |
| `task_version BIGINT`, `kind`, `silent`, `payload_text TEXT` | ClaimDue 同一 transaction 写入的不可变明文 payload 快照；Prompt/Script executor 都只读该 snapshot，更新任务不改已创建 run 的语义。 |
| `script_definition_id CHAR(36) NULL`, `script_content_hmac CHAR(64) NULL`, `script_key_version INT NULL` | 历史兼容列；新的直接 Script command run 均为 NULL，不访问 descriptor 表。 |
| `state`, `claim_token CHAR(36) NULL`, `lease_until DATETIME(3) NULL` | 仅 `claimed|queued|running|succeeded|failed|cancelled|unknown|skipped`；只有 token owner 可完成。 |
| `started_at`, `completed_at`, `duration_ms`, `error_code` | 执行证据。 |
| `thread_id`, `turn_id`, `synthetic_message_id` | Prompt 绑定；Shell 均为 NULL。 |
| `exit_code`, `stdout_hmac`, `stderr_hmac`, `output_bytes`, `output_truncated` | Script 结果 metadata，正文不入库。 |
| （无 delivery 字段） | 执行生命期与外部交付分离；每次可见发送都由下表单独记录。 |

`claimed` 只覆盖 scheduler 到 dispatcher 的 30 秒短窗口。dispatcher 必须先以 claim token 条件将 run 置 `queued`，再发布给 `Worker.Accept` 或启动 Script executor；Worker/Shell 实际获得 CPU 时条件置 `running` 并每 30 秒 heartbeat lease，terminal/delivery 前均验证 token。这样 Worker 不会在仍为 `claimed` 时抢先开始。租约过期或启动恢复时，`claimed`、`queued`、`running` 均条件标 `state=unknown,error_code=unknown_interrupted` 并释放 lease；不调用 Worker、不启动 Shell、不推消息。因为进程可能在 claim 后、dispatch 前崩溃，`claimed` 同样不能自动重放；`unknown` 从不自动重试。任何 executor 只读 run snapshot，绝不回读 mutable task payload；task update/disable 不改变已领取 run。

#### `scheduled_task_deliveries`

一条非静默 run 先建立唯一 `UNIQUE(run_id, delivery_kind, attempt)` 的 `result_card` intent；静默 run 建立 `result_card,suppressed` 记录而不外呼。字段为 `id, run_id, delivery_kind(result_card|fallback_text), attempt, outcome, stage, feishu_bot_message_id_hmac, created_at, completed_at`。`stage` 仅为 `pending|in_flight`，`outcome` 为 `sent|rejected|unknown|suppressed`（pending/in-flight 时 outcome 为 NULL）。发送前 transaction 条件领取 `pending`，外呼后条件收尾；进程在 `in_flight` 且无明确平台确认时重启，转 `unknown`，不得重发。仅 `result_card` 的明确 `rejected` 可 transactionally 新建一次 `fallback_text,attempt=1`；fallback 自己也有 unknown 边界，不能由 run 字段或 card intent 覆盖。S06 不发送“执行中”卡，故每个 run 至多一个 primary result card 和一个被拒绝后的 fallback text。

#### `scheduled_task_tool_calls`

字段与 S05 `feishu_action_calls` 对齐：app/channel/chat/thread/turn/call identity、tool、arguments HMAC、`state=claimed|in_flight|succeeded|rejected`、明文 result、created/completed 时间；唯一键为 `(app_id,thread_id,turn_id,call_id)`。旧 `result_enc/result_key_version` 仅保留迁移兼容。它只保护 schedule namespace，不能与 Feishu 外部副作用 ledger 混用。

#### `scheduled_script_definitions`

该表来自已应用的历史 migration，保留以兼容既有数据库，新的 S06 runtime 不读取、不写入也不要求管理员登记。新 Script task 的 `payload_text` 直接是 `command`。

### 6.2 Agent 工具契约

三个工具均在 `namespace: "schedule"`，只由已绑定、active 的 `item/tool/call` 调用。Runtime 必须先校验 exact `threadId + turnId + attemptId`，再从 attempt 的 `ActorPrincipal` 推导 owner；actor 缺失/混合时只返回 `schedule_owner_ambiguous`。任何带有 `app_id`、`chat_group_id`、`creator_open_id`、`sender_open_id`、`reply_target`、`workspace_dir`、`shell_path` 或凭据的 arguments 一律作为 schema `additionalProperties:false` 拒绝。

| Tool | 输入 | 成功 `inputText` | 失败 |
|---|---|---|---|
| `schedule.list_own` | `{page_size?:1..100,cursor?:opaque}` | 稳定 `(updated_at,id)` 顺序的一页：`id,kind,prompt|command,cron_expression,timezone,silent,enabled,version,next_run_at,last_run_state,next_cursor?`。cursor 以 owner HMAC 签名，跨 owner/过期 cursor 拒绝。 | `schedule_list_failed|schedule_invalid_cursor`。 |
| `schedule.create` | `{kind:"prompt",prompt,cron_expression,silent}` 或 `{kind:"script",command,cron_expression,silent}` | 新 `task_id,version=1,next_run_at,kind,silent,enabled:true`；脚本能力关闭时拒绝。 | `schedule_invalid_cron|schedule_invalid_payload|schedule_scripts_disabled|schedule_quota_exceeded|schedule_store_failed`。 |
| `schedule.update` | `{id,version,kind?,cron_expression?,silent?,enabled?,prompt?}` 或 `{id,version,kind?,cron_expression?,silent?,enabled?,command?}`；`id` 直接复用 `list_own` 返回值，`kind` 仅可复用 list 返回的原类型，payload 字段也只允许匹配现有 kind，二者都不能改变类型。 | 新 `version,next_run_at,kind,silent,enabled,effective_from:"next_unclaimed_slot"`，并返回不可撤销旧 run 的 count/state。 | `schedule_not_found|schedule_version_conflict|schedule_invalid_payload|schedule_scripts_disabled|schedule_store_failed`。 |

JSON schema 还限制：trim 后 Cron 恰好五个 ASCII whitespace 分隔字段且 1–128 bytes，Prompt/Script `command` 均为 1–16 KiB UTF-8；更新至少一个可变字段；Prompt 与 command 不能同时出现。创建/更新 transaction 中先锁 owner/task、parse Cron、写 payload/task/version、完成 tool result 和 `next_run_at`，最后同 transaction commit；commit 后向 scheduler 发本进程 wakeup，wakeup 丢失最多由 1 秒 scan 补偿。

`list_own` 对每个合法 owner task 返回明文 `prompt` 或 `command`，使 Agent 真正能够取得并管理**所有**自己的任务；该明文只存在于 exact owner attempt 的 tool result，绝不写普通 Message、日志、Langfuse、workflow 或 delivery card。cursor 确保第 101 项以后仍可逐页读取和 update。

### 6.3 调度、claim 与恢复

```
create/update tool transaction
  -> scheduled_tasks(next_run_at=cron.Next(now))
  -> scheduler.Wake()

every 1 second / startup wake
  -> SELECT enabled, route-valid tasks WHERE next_run_at <= now (+ bounded batch)
  -> within 60s grace: ClaimDue transaction -> INSERT task_run(task_id, scheduled_for) unique
     -> short claim lease; Prompt budget / Script concurrency reservation
     -> set task.next_run_at = cron.Next(scheduled_for)
  -> beyond grace: INSERT task_run(state=skipped_misfire)
     -> set next_run_at = cron.Next(now) // first future slot, no catch-up burst
  -> task kind dispatch
```

Scheduler 只扫描当前 `next_run_at` 并按 `(next_run_at,id)` 小批（默认 100）领取；不会为长停机期间的每一个历史 slot 建 run。`ClaimDue(now, taskID, observedVersion, observedSlot)` 是一条 transaction：`SELECT ... FOR UPDATE` 锁 task，重校验 `apps.enabled AND chat_groups.schedule_enabled AND task.enabled AND task.version=? AND task.next_run_at=?`，创建唯一 run、推进 future `next_run_at`、写 claim token/短 lease 和明文 payload snapshot；任何条件不符即 rollback/re-read。已 claim/queued/running 的 run 只采用 claim 时的 snapshot，之后的 task update/disable 不取消它。这是避免半执行脚本或已启动 Turn 被悄悄改语义的明确边界。

Prompt claim 仅 30 秒；Worker 实际开始时的 running lease 默认为 `worker.in_process_timeout + 30s`，Script 为 `scripts.timeout_seconds + 30s`，均每 30 秒续租。没有持有 token 的 goroutine 禁止执行或交付。每 tick 最多 20 个 Prompt dispatch、全局最多 2 个 Script running；额度/Worker pool/queue 满时 run 条件收尾为 `state=failed,error_code=failed_capacity|failed_enqueue`，不放入无界等待队列。进程优雅 shutdown 停止新 claim、取消未开始 dispatch、等待最多 `worker.stop_grace_seconds`，余下 run 由下一启动 reconciliation 标 `unknown_interrupted`，从不恢复执行。

### 6.4 Prompt 执行与频道交付

Scheduler 解密快照 payload 后构造一个不可由飞书 ingress 伪造的 `worker.Message`：

```
Message{
  ID: synthetic_message_id, TraceID: new trace, ChatGroupID: task.chat_group_id,
  Key: stored app/channel-derived key, Runtime: stored AppRuntime,
  Reply: group -> {chat_id,"chat_id"}; p2p -> {creator_open_id,"open_id"},
  ActorPrincipal: creator_open_id (memory-only, never logged/persisted in Message),
  Query: prompt, ReceivedAt: scheduled_for, ScheduledTaskRunID: run_id,
  ExecutionProfile: ScheduledPrompt{run_id,claim_token,silent|task_result_card},
}
```

Ingress 与 scheduler 都必须填 `ActorPrincipal`；Worker 普通 drain 只能合并连续同 Actor 的 Message，Actor 切换是 Batch 边界。`ScheduledPrompt` 更是永远独占。Scheduler 在调用 `Accept` 前按 claim token 条件转 `queued`；Worker CPU 开始时按 run token 转 `running`、在 `turn/start` accepted 后保存 `thread_id/turn_id`。若 Accept 被拒绝，queued run 以同一 token 条件收尾 `failed_enqueue`。该 profile 使用独立 `ScheduledRunLifecycle`，**禁止**调用普通 `messages` Lifecycle、S04 companion Lifecycle 或普通 `OutputForBatch`。非静默 Prompt 仅发送一张自己的终态静态任务 card，含 App Server final result；所有 intent 先持久化到 `scheduled_task_deliveries` 再外呼。静默 Prompt 只创建 suppressed delivery record，不创建普通/任务 card、不调用 text fallback，但仍完整运行和持久化 terminal metadata。

如果 Worker queue full、pool saturated、App Server unavailable 或 synthetic route 缺失，run 不启动 Turn，写 `state=failed,error_code=failed_enqueue|failed_runtime|failed_route`。Worker queue item 具有 `OnDiscard(runID,claimToken)`：任何 `/cancel`/`/new` control、worker timeout、shutdown 清队都以 token 条件收尾 `state=cancelled,error_code=cancelled_by_channel_control`，绝不留下 queued run。active Prompt 被 control interrupt 时也走同一 terminal API；这不改变 task 定义或取消未来 schedule。脚本 run 不属于 channel Worker active Turn，故 `/cancel`/`/new` 不杀 Shell；未来若需要此控制另开 Script Control Story。

### 6.5 Script 执行与结果交付

```
claimed script run
  -> conditionally persist queued using the claim token
  -> load direct command snapshot and App workspace
  -> exec configured shell_path -c command in a new process group as the bot process user
       Dir=workspace_dir; normal process environment is retained
  -> concurrently drain stdout/stderr to bounded writer; timeout/cancel kills and reaps the full group
  -> state=succeeded if exit 0 else state=failed,error_code=script_exit|script_timeout
  -> persist result_card delivery row: silent ? outcome=suppressed : stage=pending -> SendStaticCard(summary)
```

`schedule` 配置声明 `tick_interval_ms=1000`、`misfire_grace_seconds`（1–3600，default 60）、`payload_keys`、`owner_hmac_keys`、`max_enabled_tasks_per_owner=100`、`max_enabled_tasks_per_app=1000` 和 `max_prompt_dispatch_per_tick=20`。`scripts` 只声明 `enabled`（default false）、absolute `shell_path`、`timeout_seconds`（default 300, 1–1800）、`max_output_bytes`（default 24 KiB, 1–64 KiB）与 `max_concurrent=2`。启动校验只确认 shell 为 absolute regular executable 和这些运行可靠性限制有效；不校验管理员 registry、降权或网络隔离。每个 executor goroutine 在最外层 `recover`，将 panic 条件收尾为 `state=failed,error_code=failed_executor_panic`；Cron library 的 default panic behavior 不得让 lease 静默遗留。

脚本 stdout/stderr 持续并发 drain 到受限 writer（不能用会整体累积的 `CombinedOutput`），最多保留 `max_output_bytes+1` 以判断截断；卡片正文使用 UTF-8 safe conversion 和 Markdown escape，并固定标明 `exit_code`、是否截断、开始/结束时间和上限内 console。不得回显 script path、环境变量或 console 至日志/DB/Langfuse/workflow。非零 exit 仍可交付结果卡，但 run 是 failed；卡片发送 unknown/rejected 不改变执行终态。

### 6.6 错误、隐私与观察性

| 场景 | 动态工具/用户可见结果 | 持久化与日志 |
|---|---|---|
| Cron/payload/schema 不合法 | tool `success:false` 的稳定错误；不写 task。 | call ledger rejected；仅 tool/error code。 |
| 非 owner / task 不存在 / version 不匹配 | `schedule_not_found` 或 `schedule_version_conflict`；不泄漏存在性或 payload。 | 无 task 改动；记录 call digest。 |
| payload key 加解密失败 | tool 安全失败；已有 task due 时不执行。 | `failed_payload_unavailable`，只记录 task/run ID。 |
| due claim 冲突/lease 过期 | 不向用户推送。 | 条件失败重读；旧执行为 `unknown_interrupted`，不重试。 |
| Worker / App Server 失败 | 非静默 task result card 说明失败，卡失败可 fallback 一次 text。 | run terminal + error code/thread/turn；无 Prompt。 |
| Shell timeout/non-zero/capture 截断 | 非静默结果卡显示安全摘要。 | exit code/digest/byte count/truncated；无 console 明文。 |
| 飞书 card/text delivery rejected/unknown | 不重复发送。 | `scheduled_task_deliveries` 的对应 intent `outcome`；执行终态不被覆盖。 |

每个 event 使用 `event=scheduled_task_*`、`app_id`、`channel_key`、`chat_group_id`、`task_id`、`run_id`、`kind`、`silent`、`state`、`error_code`、`trace_id`、`thread_id/turn_id`（存在时）、duration 和 payload/output digest/size。禁止 `creator_open_id`、Prompt、script_ref、console、card body、token、credential、shell environment、真实 message ID 进入 `slog`、workflow JSONL 或 Langfuse；Langfuse 仍 metadata-only/fail-open。

## 7. 测试设计与验收标准

| 编号 | Given / When / Then |
|---|---|
| S06-AT-00 | Given a persisted `s05-feishu-v2`, `s06-schedule-v1`, `s06-schedule-v2`, or `s06-schedule-v3` Thread and each crash point of catalog upgrade, When worker reaches its idle boundary or startup recovers `archive_pending|start_pending`, Then durable `chat_groups` upgrade state atomically records old pointer/target, archives old Thread idempotently, CAS-clears it, persists only a new `s06-schedule-v4` Thread with `feishu + schedule.list_own/create/update`; an unpersisted start result is orphaned and never resumed, and `thread/resume` always retains the full catalog in a real L3 schedule tool round-trip. |
| S06-AT-01 | Given valid five-field Cron, invalid six-field/macro/impossible expression and fixed `Asia/Shanghai` clock, When create/update, Then valid `next_run_at` is deterministic UTC and invalid inputs make no DB write. |
| S06-AT-02 | Given same/different app, channel and creator routes plus group A/B and a synthetic A Prompt, When list/update by an active tool call, Then only exact ActorPrincipal owner tasks are visible/mutable; forged identity fields, absent/mixed actor Batch and cross-owner IDs reveal nothing and cause no update. |
| S06-AT-03 | Given duplicate `item/tool/call` with same or different arguments digest, DB crash before/after mutation and expired in-flight claim, When create/update/list, Then exactly-once transactionally coupled task mutation/replayed result/conflict/reconciliation semantics hold and reader never blocks. |
| S06-AT-04 | Given create then optimistic update/disable/re-enable, stale version, owner/App quota and 101+ tasks, When concurrent calls/list cursor race, Then version increments once, stale update returns conflict, all owner tasks are paginable/manageable, disabled task has no future next run, and only already claimed/queued/running immutable snapshot may complete with `effective_from=next_unclaimed_slot`. |
| S06-AT-05 | Given due rows, two scheduler ticks/restarts, task update/disable and a unique task slot, When `SELECT FOR UPDATE` ClaimDue races with them, Then one run has one claim token and one dispatch, execution reads only its immutable plaintext payload snapshot, and terminal update without token is rejected. |
| S06-AT-06 | Given tick delay inside/outside misfire grace and long downtime, When scheduler reconciles, Then inside grace runs once; outside grace records one `skipped_misfire`, advances to future, and creates no historical replay burst. |
| S06-AT-07 | Given same-channel ordinary A/B prompts before/after scheduled A Prompt and another channel due simultaneously, When dispatcher runs, Then actor change and ScheduledPrompt are exclusive FIFO Batch boundaries with original route/owner; ScheduledRunLifecycle—not normal Message Lifecycle—records queued/running/terminal, no cross-channel blocking or direct Processor bypass occurs. |
| S06-AT-08 | Given silent/non-silent Prompt in work and companion apps, deterministic App Server final text, success/failure/timeout, card rejection/unknown and DB failure around send, When Worker completes, Then silent has no automatic Feishu send, non-silent has only its own static result card containing final text, normal S04 card ownership stays intact, and per-delivery intent/outcome agree without unknown replay; only explicit card rejection creates one separately tracked text fallback. |
| S06-AT-09 | Given channel `/cancel`/`/new`, worker timeout or shutdown before/during scheduled Prompt, When queue control wins, Then active run is interrupted and queued item `OnDiscard` conditionally ends run `cancelled_by_channel_control`; no queued state remains and future task definition remains enabled. |
| S06-AT-10 | Given a direct local command, scripts disabled, forked child, infinite dual-stream output, exit 0/nonzero/timeout and output > limit, When scheduler executes, Then the command starts once as the bot process user in its App workspace; timeout/cancel kills and reaps its process group, while non-silent card contains console marker/exit/truncation and silent sends no automatic message. |
| S06-AT-11 | Given graceful shutdown or process crash after claim/queue/start, When next process reconciles, Then no unknown Prompt/Shell is replayed; stale claims are classified as `unknown_interrupted`, new future slots remain schedulable. |
| S06-AT-12 | Given DB, MySQL fake, slog/workflow/Langfuse fake and card fake across all paths, When inspect records, Then no Prompt、script command、console、secret or raw Feishu external ID is exposed outside the non-silent result card; owner HMAC remains scoped to the exact owner query/cursor/replay route. |

测试层次：L1 为 Cron/parser/schema/path/output redaction/state-machine；L2 为 sqlmock/MySQL repository、tool handler/replay ledger、scheduler/Worker fake/App Server fake；L3 是本机 `codex app-server --stdio` 对新增 catalog 与 `item/tool/call` round-trip、Docker MySQL migration，以及临时 schema 上双并发 `ClaimDue` 的唯一 slot/run 与 restart 非重放证据；L4 是测试飞书 App 的同群/同 p2p 非静默 Prompt 卡与非静默 Script 结果卡。涉及 scheduler/Worker/runtime 并发的目标包必须 `go test -race`。

## 8. 最终本地集成校验

### S06-LI-01：Agent 创建、触发与交付一条 Prompt 和一条 Shell 任务

**前置**：新 migration 已在 Docker MySQL 应用；`scripts.enabled`、absolute executable `shell_path`、timeout/output/concurrency 配置通过启动校验；新 bot process 的启动时间/版本已记录，`/healthz=ok`、`/readyz` receiver connected；测试 App 有 interactive message 权限；本机 App Server 已登录。

| 步骤 | 操作 | 预期与保存证据 |
|---|---|---|
| 1 | 在测试 p2p 或群发送唯一标识任务，要求 Agent 使用 `schedule.create` 创建一分钟内的 `silent=false` Prompt，并 `list_own`。 | Agent tool result 含 task metadata；DB 中只有该 owner 的 plaintext payload 和 future `next_run_at`。保存 task ID digest、call ID、trace、migration version。 |
| 2 | 等待 trigger，并在同频道紧随其后由另一群成员发送一条普通文本；Prompt 要求返回唯一 final 标识。 | 计划 Prompt 与两位成员普通文本严格按 actor/FIFO 边界处理；计划 run 生成独立 task result card，且卡含唯一 App Server final 标识。保存用户可见卡截图/body HMAC、message ID HMAC、run/thread/turn/trace、对应 delivery-row ID/stage/outcome。 |
| 3 | 以直接 `command` 创建一个 `silent=true` 和一个 `silent=false` Script task。 | 两个 Shell 都各执行一次；静默任务无飞书自动消息且 delivery row 为 `suppressed`，非静默静态结果卡包含唯一 console 标识、exit code 和截断状态，run 有 output HMAC，delivery row 有独立 outcome。 |
| 4 | 用另一个群成员或频道让 Agent `list_own/update` 第一步 task。 | 不返回/修改第一位 owner 的 task；保存安全错误、DB unchanged evidence。 |
| 5 | 重启新进程前制造一个已 queued/running 未完成的 fake run，并停用测试 App。 | 新进程健康、scheduler reconcile 将旧 run 标 `unknown_interrupted` 而不产生飞书/脚本/Turn 副作用；停用 App 后 due slot 记 `failed_route_revoked`。保存新进程时间、DB row 和日志 metadata。 |

失败时按“dynamic tool binding → plaintext task transaction → due scan/claim → Worker/Shell → App Server → delivery”保存新进程时间、task/run/call digest、trace、状态和稳定 error code。旧进程、静态 fake 或仅 `go test` 不是 L3/L4 证据。

## 9. Definition of Done

- [x] Story List 的 S06、HLD 和本 Story 都以 MySQL + Agent dynamic tools 为同一规范，历史命令草稿已显式 Superseded，不存在两个活跃 S06。
- [x] `scheduled_tasks`、`scheduled_task_runs`、`scheduled_task_deliveries`、`scheduled_task_tool_calls` 以及 ChatGroup catalog-upgrade 字段的 immutable migration、repository、owner-scoped HMAC、snapshot/outbox、索引、状态/lease/restart reconciliation 和参数化 SQL 有 L1/L2 证据；`scheduled_script_definitions` 是不再使用的历史表。
- [x] 三个 schedule tools 的 schema、可信 ActorPrincipal route owner check、transactionally coupled at-most-once replay、version conflict、cursor pagination、Cron/payload validation 与 `s05 → s06` tool catalog upgrade/resume persistence 均通过 fake/L3 验证；真实 v4 Thread 已完成 `list_own → update(id,version)`。
- [x] Prompt run 只经 actor-preserving 原 channel Worker FIFO 和 ScheduledRunLifecycle，queue control/shutdown 不遗留 run；Script run 以当前 bot OS 用户直接执行已 claim 的 `command`，有 process-group、timeout、输出截断和并发限制。
- [x] 静默和非静默的 Prompt/Script 终态、卡/text fallback、unknown/rejected 不重发、日志/Langfuse redaction 均有测试；非静默真实 Prompt 和 Script 各有修复后新进程的 L4 卡片证据。
- [x] `gofmt`、`go vet ./...`、`go test ./... -count=1`、相关 `go test -race`、migration/新进程 health/ready DB read-back 都有当前证据；不将 fake/L3 伪称 L4。
- [x] `config.yaml.template`、README、HLD、Story List、运行手册和残留风险同步；独立技术架构、质量/安全、产品契约 review 的 blocker 已整改并 blocker-only 复审。

## 10. 风险、残留项与后续 Story

| 风险/残留 | 当前控制 | 后续责任 |
|---|---|---|
| Cron library/schema 行为漂移 | 实施前锁定版本，固定 parser options，L1 DST/next-time tests；`Asia/Shanghai` 无 DST 仍验证。 | Dependency upgrade gate。 |
| 单进程 scheduler 不可用 | MySQL `next_run_at`/claim/lease 是事实源；启动 reconciliation 无重放。 | 多实例 HA Scheduler Story（需 leader/election 设计）。 |
| 直接 Script command 的本机权限 | 用户明确允许任意本机 command；执行账户就是 bot 进程账户，命令拥有该账户本来具有的网络与文件权限。 | 若未来部署给他人或多租户，必须另开 capability/isolation Story，不能静默复用本设计。 |
| 静默 Prompt 的显式飞书工具仍可发消息 | 仅抑制自动终态交付，工具行为维持 S05 合约并可审计。 | “strict silent/no external action” policy Story。 |
| 停机历史 slot | 明确 skip-misfire，无补跑。 | 用户需要 catch-up/补偿时单开 delivery semantics Story。 |
| Owner open ID 存储 | 个人本机部署明文保存 + HMAC 索引用于 synthetic route、owner 查询与 cursor/replay；ActorPrincipal 不写日志。 | 多用户授权、owner deletion/retention Story。 |
| 用户需要删除、读回 Prompt、时区/一次性任务 | 本期 intentionally absent，禁用即软停止。 | Schedule lifecycle UX Story。 |

### 设计依据

- [Story 从设计到 Delivered](../sop/story-design-to-delivery.md)
- [Story 撰写规范](STORY_WRITING_SPEC.md)
- [Codex App Server 协议调研](../01-codex-appserver-protocol-research.md)
- [Codex Workspace Bot 概要设计](../02-redesign-high-level.md)
- [S05：附件输入与飞书能力代理](S05-附件输入与飞书能力代理-设计.md)（dynamic tool 的 route、single-writer、ledger 与 redaction 边界）
