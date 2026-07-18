# S08：Langfuse 全链路 Trace 可观测性

> 状态：In Development（2026-07-14；realtime 明文 Project、P0 read-back 与真实飞书 Trace 已验收；scheduled/故障注入验收待后续执行）
> 关联概设：[重构高层设计](../02-redesign-high-level.md)（本 Story 实现时需同步可观测性契约）
> 依赖：S03 App Server Runtime、S06 定时任务、现有自托管 Langfuse 实例、一个新建的 Langfuse Project 与其 API Key

## 1. 一句话目标

让操作者能在既有自托管 Langfuse 的**新 Project**中按一次实际 Codex 执行（一个 Batch/Turn）或一次定时脚本的既有 canonical `trace_id` 查看完整执行树、会话顺序、Agent 工作环节、工具调用、可见进展和最终答复，从而定位请求为什么慢、失败或产出异常。

## 2. 背景

当前运行时已具备两类可复查事实，但没有统一的可查询 Trace：

- `messages.trace_id` 是实时入站的 32 位十六进制稳定标识；S03 也已记录 `app_id`、频道、Thread、Turn、Item 与事件路由结果。正常文本可被 Worker 合并为一个 Batch，而一个 Batch 只会发一次 `turn/start`。
- S03 的 `appserver-raw/event/outcome` JSONL 能保留完整 App Server 原始事件，但它是仅本机 debug 工件，且按进程文件而不是按会话浏览；S06 的 prompt run 目前没有可供 Langfuse 使用的持久化 32 位 Trace ID，script run 也不经过 Codex Worker。

旧 `/root/cc_workspace_bot` 的 Langfuse 方案证明了“每个 turn 一个 trace、会话 ID 聚合多 turn、工具为子 observation、稳定 ID 幂等、失败 fail-open”的价值，但它解析的是 Claude transcript，不能直接搬到 Codex App Server。

本 Story 采用 Langfuse 的 OTLP/HTTP 接入。Langfuse 对 Go 没有原生 SDK；官方支持用 OpenTelemetry HTTP exporter 把 `gen_ai.*` generation 与普通 span 发送到 `/api/public/otel`。Langfuse 的推荐粒度也正是“聊天每 turn 一个 trace、整个会话一个 session”。

### 2.1 调研边界与事实

S03 的真实 664 行样本已观察到：`turn/started`、`turn/completed`、`item/started`、`item/completed`、`item/agentMessage/delta`、`item/commandExecution/outputDelta`、`turn/diff/updated`、`thread/tokenUsage/updated`、`hook/*`、Thread 状态、MCP 启动、告警及账户 rate-limit 更新。当前 Runtime 另已处理 App Server server request `item/tool/call`。

App Server 的公开模型是 Thread → Turn → Item；它会流出 agent message、命令、文件变更、工具及进度事件。它**不承诺**返回模型的隐藏 system/developer prompt、隐藏 chain-of-thought，或每次底层模型调用的独立 prompt/token 账单。因此本 Story 只记录协议实际提供的原始文本/结构，并明确标注不可取得字段；不得虚构“完整 reasoning”或把 Thread 级用量伪装为单次 generation 用量。

## 3. 范围

### 3.1 本期做什么

1. 在已有 Langfuse Organization 下创建一个隔离的新 Project（建议显示名 `codex-workspace-bot`、30 天 retention）及一对只供本 Bot 使用的 Project API Key；旧 CC Workspace Bot Project、Key 和历史 Trace 均不改动。
2. 增加可关闭、fail-open 的 `observability`/OTLP 适配器；每个 realtime **Batch/Codex Turn**建立一个 canonical execution Trace，使用首条 source message 的既有 32 位 `trace_id`；不将一套 Item/工具树复制到多条 message Trace。
3. 直接用明文 `(app_id, chat_type, chat_id)` 组成 Langfuse Session ID，并把 OpenID、Chat ID、channel key 等真实检索字段写入 metadata，令操作者能直接按真实会话、用户和应用排查。
4. 将 App Server 的 Turn 与 Item 流投影为有界的 Trace 树：根 turn、每次可证明的 Agent loop、工具/命令/文件子 span、可见 agent 输出 generation、终态和最终 token 快照。
5. 在这个个人自用的新自托管 Project 中，默认且唯一地以**明文**记录 App Server 协议实际提供的用户输入、agent 输出、reasoning Item、工具参数/结果、命令输出、飞书/调度业务标识和附件/文档相关正文；不再设置 `content_mode`、HMAC、ID digest、内容分类或业务数据脱敏开关。
6. 写入可直接查询的业务维度：`app_id`、`workspace_mode`、`chat_type`、`task_type=realtime|scheduled`、`command_kind`、`scheduled_task_id`（若有）、`channel_key`、`chat_id`、`user_open_id`、Thread/Turn/Item ID、运行版本与终态错误码。
7. 以 L1/L2/L3 证明 Batch 关联、ID、树形关联、原文收集、明文可读性、失败降级和真实 Langfuse 查询；实施时同步更新配置模板、运行手册、AGENTS、HLD 和 Story List。

### 3.2 本期不做什么

- 不向 Langfuse 写隐藏 chain-of-thought、未由 App Server 返回的模型 system/developer prompt，或臆测的每调用 token/cost；这些数据没有权威来源。
- 不把 S03 raw JSONL 删除、替换成 Langfuse 或承诺 Langfuse 是逐行原始事件归档；raw/event/outcome 仍是协议排障证据。
- 不追踪 Langfuse 自身 HTTP 调用、MySQL、飞书 SDK 的所有低价值内部 span，不引入 OTel Collector、Kafka 或 Redis；仅为幂等的 Turn/Session usage 累计增加本 Story 明确定义的两个 MySQL ledger。
- 不在本 Story 接入 Langfuse Prompt Management、Dataset、Experiment、在线评分、告警或模型定价治理；这次先保证 Trace 数据模型正确。
- 不复用旧 CC Workspace Bot 的 Key，不迁移旧 Project 的历史 trace，也不把 API Key 写进仓库、日志、MySQL、JSONL 或飞书消息。

## 4. 依赖与前置条件

| 依赖 | 状态 | 本 Story 用法 |
|---|---|---|
| S03 Runtime 与 Timeline | Delivered | 从单一 stdout reader 获得已分类的 App Server event、route 和终态；Timeline 仍保留本机 debug 证据。 |
| S04/S05 输出与受控工具 | Delivered | `agentMessage`、命令、file change、`item/tool/call` 与工具 response 的事件来源。 |
| S06 调度器 | Ready for final local validation | 为 scheduled prompt 建立合规的 32 位 Trace ID，并写入 `task_type=scheduled`。 |
| 已有自托管 Langfuse | 已存在 | 只复用实例/Organization；不复用旧 Project Key。 |
| 新 Langfuse Project 与 Key | 人工前置 | 操作者在 Project Settings 创建；名称、retention 与密钥只写本机 secret 配置。 |

### 4.1 新 Project 建立手册（实施前人工关卡）

1. 登录**现有自托管实例**，在原有 Organization 下新建 Project；本设计将“新 Workspace”落实为 Langfuse 的 Project，因为 Project 才拥有独立数据和 API Key。
2. 默认建议 Project 名为 `codex-workspace-bot`、环境 `development`、retention 30 天；操作者可在创建前调整 retention，选择结果必须写回本 Story 的交付证据。
3. 在该 Project Settings 生成新的 public/secret key；仅写入 gitignored `config.yaml` 或进程环境 `LANGFUSE_PUBLIC_KEY` / `LANGFUSE_SECRET_KEY`，不得显示在终端输出、文档或截图。
4. 以新 Key 发送一条带唯一 nonce 的预检 Trace；确认它只出现在新 Project，旧 Project 无新增数据，再启用 Bot 的真实写入。
5. 在实现前记录 Langfuse 版本（OTLP endpoint 要求至少 `v3.22.0`）、Project access 成员、retention、Key 创建者和 revoke/rotation 路径；这些是 S08-LI-01 的证据，不记录 key 值。

## 5. 核心设计决策

| 问题 | 结论 | 原因 | 后果 |
|---|---|---|---|
| 接入协议 | 直接使用 Go OpenTelemetry SDK + OTLP/HTTP protobuf 到 `${base_url}/api/public/otel/v1/traces`，携带 Basic project-key auth 与 `x-langfuse-ingestion-version: 4` | Langfuse 官方支持 Go 的该路径；OTLP ingestion 才支持 observation-level evaluator | 新增 OTel 依赖；禁止 gRPC 和 legacy `/ingestion`。 |
| Trace 主键 | `trace_id` 必须为 32 位小写 hex，直接作为 W3C 16-byte Trace ID；不得另造 Langfuse ID | realtime 已满足该格式，可与 MySQL/日志一跳关联 | S06 36 字符 UUID Trace ID 改为 32 hex 随机 ID；旧格式输入在适配器处拒绝并计数，绝不悄悄重写。 |
| Trace/Session 粒度 | 一个 Batch/Codex Turn 一个 canonical execution Trace；一个 scheduled script run 一个无 Codex 的 Trace；一个 `(app, chat_type, chat_id)` 一个 Session | 一个 Turn 的 Item/工具树只能属于一个 OTel Trace，不能在多条 message Trace 中复制 | Batch 的 canonical Trace ID 是首条 message 的既有 ID；每个 source message 以其真实 message ID、position 和原文关联。 |
| 身份字段 | `user_id=sender_open_id`；`session_id=app_id + ":" + chat_type + ":" + chat_id`；频道/Chat/OpenID 均明文写入 | 操作者需要从 Langfuse 直接按真实飞书用户和会话排查 | 这是个人自用的新自托管 Project 的明确数据策略；不得再加 HMAC、hash、digest 或 ID 转换层。 |
| 原文采集 | 所有 App Server/飞书/调度**业务数据**以明文写入：请求、响应、reasoning、工具参数/结果、文档/附件正文、业务 ID、路径与 URL | 用户明确需要完整排障上下文 | 不提供 metadata-only 或内容脱敏模式；实施同一变更必须同步 AGENTS/HLD，并将 S07 中“Goal 不进 Langfuse”的旧规则明确改为仅约束其余 Bot-owned surfaces。唯一排除运行凭据：Langfuse Key、Authorization/Cookie、飞书 access token 和进程环境 secret，不能进入 Trace、日志或仓库。 |
| Agent loop 定义 | 一个 `reasoning` Item 开始一个 `agent.loop`，至下一个 reasoning Item 或 Turn protocol terminal 结束；没有 reasoning 的 Item 归入 `agent.loop.unknown` | App Server 未提供底层模型循环 ID，reasoning Item 是唯一可验证边界 | `agent.loop` 使用 `generation` observation，以承载该 loop 的 usage；metadata `semantic_kind=agent_loop`，loop 计数只表示协议可见工作段，不伪称一个底层模型调用。 |
| 模型 generation/usage | 以 `agentMessage` Item 建 `codex.agent_message` generation；输入为可取得的协议上游内容，输出为按 Item ID 合并后的 delta/完成文本；以 `agent.loop` generation 承载该 loop 的观察到的 usage | 既保留模型可见输出，又不捏造隐藏 prompt 或每次底层调用的精确 usage | `model_input_available=false` 时输入为 `null` 并写 `unavailable_reason=app_server_not_exposed`；不写 generation cost。 |
| 写入可靠性 | Runtime reader 只做有界内存采集；导出由有界、非阻塞 BatchSpanProcessor 完成，错误/满队列只写不含凭据的计数日志 | Langfuse 不可用不能拖慢、失败或重放飞书/Codex 请求 | telemetry 在 exporter 故障时可丢失，必须有 `observability_dropped` 指标；不以“已写 Langfuse”决定 message 状态，且不跨进程补发已关闭 Trace。 |
| Project 绑定 | 首次启用、key rotation 或 Project 改动时，在目标 Project selector 内以唯一 nonce write/read-back 做人工绑定 | Project-scoped Key 用自身 API 无法证明“我属于哪个 Project”；不能作出不可实现的自动归属验证承诺 | 绑定失败时禁用 exporter，不改变业务请求；不以 hash/fingerprint 或 metadata-only 降级替代人工确认。 |

## 6. 主链路与数据/接口契约

### 6.1 ID、Session 与检索契约

```text
realtime Message(s)                      scheduled claim
  -> existing 32hex message trace IDs      -> INSERT/claim `scheduled_task_runs.trace_id`
  -> Batch chooses first as canonical       -> crypto/rand 32hex, one scheduled execution
  -> one `turn/start`                       -> prompt: Worker/Codex Trace; script: scheduled.script Trace
  -> Langfuse TraceContext(canonical trace_id, session_id, search metadata)
```

```text
session_id  = app_id + ":" + chat_type + ":" + chat_id
user_id     = sender_open_id
chat_id     = chat_id
channel_key = channel_key
```

所有值均按业务原值明文写入。`session_id` 与 `user_id` 是 Langfuse 一等字段；其余写入每个 observation 的顶层、可筛选 metadata。每个 span 继承相同的 Trace 属性（而不只写根），因为 Langfuse 的筛选/聚合可发生在 observation 级别。

`worker.Batch` 的 canonical Trace ID 是 `batch.Messages[0].TraceID`，并且只产生**一棵**执行树。root metadata 写 `source_message_count`、`source_first_message_id` 与有上限的 `source_message_ids`；完整 source message/position/原文映射按 Batch Formatter 的实际顺序写入 root input。非首条 message 的查找先以其 MySQL `trace_id` 查到 batch/canonical ID，再在 Langfuse 打开 canonical Trace。不得 fan-out 复制相同 output、工具、token 或 delivery observation。

prompt/script scheduled run 在首次 claim transaction 把 `crypto/rand` 的非零 32hex 填入 `scheduled_task_runs.trace_id`；migration 增加唯一索引且 claim 使用条件 `trace_id IS NULL` 更新，并将该值、真实 task owner、chat-group route 一起返回 `ClaimedRun`。PromptDispatcher 只能复制 `ClaimedRun.TraceID` 到 synthetic Message/Batch；script executor 直接用同值。进程重启不重放、不补写 `unknown_interrupted` run；未来人工 retry 必须新建 run/new trace（可另记 predecessor trace ID），绝不复用旧 run Trace；本 Story 中“retry”仅指同一 SpanContext 的 exporter transport retry。script run 建 `scheduled.script` root + `tool.shell` span，不宣称存在 Thread/Turn/generation。

根 Trace 名固定为 `codex-workspace-bot.turn` 或 `codex-workspace-bot.scheduled-script`。tags 固定且低基数：`source:feishu|scheduler`、`task:realtime|scheduled`、`mode:work|companion|script`、`command:<kind>`（没有命令为 `command:text`）、`app:<app_id>`；动态 ID 绝不进入 tag/name。

### 6.2 Trace 树与字段选择

```text
codex-workspace-bot.turn  [SPAN; trace_id = canonical batch message trace_id]
├─ ingress.accept          [EVENT]
├─ codex.turn              [SPAN]
│  ├─ agent.loop            [GENERATION, semantic_kind=agent_loop, loop=1]
│  │  ├─ reasoning.item     [EVENT]
│  │  ├─ codex.agent_message[GENERATION, item_id=...]
│  │  ├─ tool.command       [SPAN, semantic_kind=tool] (0..n)
│  │  ├─ tool.file_change   [SPAN, semantic_kind=tool] (0..n)
│  │  ├─ tool.mcp_or_web    [SPAN, semantic_kind=tool] (0..n)
│  │  └─ tool.item_call     [SPAN, semantic_kind=tool] (0..n)
│  └─ agent.loop            [GENERATION, semantic_kind=agent_loop, loop=2] (0..n)
├─ delivery.feishu          [SPAN]
└─ turn.terminal            [EVENT]
```

`ingress.accept` 的 input 是原始用户 text（或 scheduled prompt），metadata 含入口来源、Bot 内部 `messages.id`/batch ID、真实飞书 event/user/bot message ID 与 `task_type`。根 `codex-workspace-bot.turn` 的 input 同为原始用户可见请求；其 output 在 terminal 时更新为 `{status, final_answer, assistant_progress}`，其中 `final_answer` 是最终 agent final 原文，`assistant_progress` 是已合并的可见 commentary 摘要。没有可见 final 时必须为 `null`，并写稳定 `error_code`，不得制造成功答案。

| App Server 证据 | Langfuse 投影 | input / output | 重要 metadata |
|---|---|---|---|
| `turn/started`、`turn/completed` | `codex.turn` lifecycle + `turn.terminal` event | turn input 由 ingress；output 为最终可见 answer/status | `thread_id`、`turn_id`、`status`、`elapsed_ms`、`terminal_source` |
| `item/started/completed` type `reasoning`、text/summary delta | `agent.loop` 内 `reasoning.item` event | 按 item ID 聚合的协议可见 reasoning 文本/摘要；缺内容写 `null` | `item_id`、`content_available`、`protocol_method` |
| `item/agentMessage/delta` + completed | `codex.agent_message` generation | input：仅协议实际暴露的上游 content，否则 `null`；output：completed item 的完整 text 优先，delta 仅补齐缺失部分 | `item_id`、`model_input_available`、`delta_count`、`output_bytes`、`output_source` |
| `item/commandExecution/*` | `tool.command` span | 命令/参数原文；累计 output delta + completed result 原文 | `item_id`、`exit_code`、`output_truncated` |
| `item/fileChange/*` | `tool.file_change` span | App Server 返回的 file change 原文；完成结果 | `item_id`、`change_count` |
| `item/mcpToolCall/*`、`item/webSearch/*` | `tool.mcp_or_web` span | 调用 input、progress、完成 result 的协议可见原文 | `item_id`、`item_type`、`output_source` |
| server request `item/tool/call`、handler result、response write | `tool.item_call` span | Tool name + arguments 原文；handler success/error 与写回 response 原文 | `call_id`、`tool_name`、`handler_state`、`response_write_state` |
| 其它 `item/*` | `item.generic` span/event | 保留 type、started/completed 可得 payload；不静默丢弃 | `item_id`、`item_type`、`semantic_kind=unknown_item` |
| `turn/completed.usage` | `codex.turn` 的权威 usage；同步 root/会话累计 | 不作 generation input/output | `usage_source=turn_completed`、`input_tokens`、`output_tokens`、`cached_input_tokens`、`reasoning_output_tokens`、`total_tokens` |
| `thread/tokenUsage/updated` | active `agent.loop` 的逐快照 usage delta；同步 Thread/root snapshot | 不作 generation input/output | `usage_source=thread_tokenUsage_snapshot_delta`、`input_tokens`、`output_tokens`、`cached_input_tokens`、`reasoning_output_tokens`、`total_tokens`、`unallocated_tokens` |
| `turn/diff/updated`、`hook/*`、warning | `codex.turn` event（只在状态变化/异常） | 原始结构化 payload | `protocol_method`、`severity` |
| MCP/remote-control/thread status/rate-limit | 已证实不含业务 payload 的纯运行状态可写 root 计数/最后状态；其余一律 event/generic/overflow observation | 任意参数、结果、文本或未知 payload 均完整原文；不得因高频静默丢弃 | `method_count`、`last_state`、`protocol_method` |

高频 `agentMessage/delta` 只在内存按 `item_id` 累积，结束时写一次 generation output；不得为 556 个 delta 制造 556 个 observation。每个 loop 的原始、协议可见的 Item 原文在对应子 span 中保留，因而查看一个 loop 就能看到 reasoning、工具调用/结果与 agent 输出。`item/tool/call` 必须增加 client-side lifecycle observer，分别记录 request 接收、handler 成功/失败和 JSON-RPC response 写入成功/失败；仅有入站 App Server Event 不足以证明工具结果。

#### Usage 归属与会话累计

`turn/completed.usage` 是本 Turn 的权威总量，字段为 `inputTokens`、`outputTokens`、`cachedInputTokens`、`reasoningOutputTokens` 与 `totalTokens`；所有原始 camelCase counter 以明文写入 `codex.turn`、root 与该 Turn 的 MySQL usage ledger。Langfuse 的 flat `usage_details` 必须是互斥 bucket：S03 实测表明 `inputTokens` 包含 `cachedInputTokens`、`outputTokens` 包含 `reasoningOutputTokens`，因此在算术校验 `totalTokens=inputTokens+outputTokens` 成立时，写 `input=inputTokens-cachedInputTokens`、`input_cached_tokens=cachedInputTokens`、`output=outputTokens-reasoningOutputTokens`、`output_reasoning_tokens=reasoningOutputTokens`、`total=totalTokens`。任何负数、total 不一致或未来 schema 改变都不做猜测：保留完整原始 counter，写 `usage_details_available=false` 与原因，不写可能双算的 Langfuse bucket。它不能被平均拆分到 loop。

每个 `agent.loop` 在开始时保存最后一个 `thread/tokenUsage/updated` snapshot，并在同一 attempt/loop owner 内持有按 `(generation,seq)` 去重的 `loop_usage_accumulator`；同一 loop 活跃时收到的每个后续 snapshot 都计算单调差值，恰好一次累加到该 accumulator。loop 关闭时才把**累计值**（不是最后一次 delta）写入该 loop generation 的 `langfuse.observation.usage_details`。delta 同样先做上述互斥 bucket 校验，原始 snapshot/delta 永远明文保存为 observation/event；没有 snapshot、计数回退、跨 loop/Turn 边界或无法确定所属时，必须写 `usage_available=false`、`usage_source=unavailable_or_unallocated` 和原始 snapshot，不得臆造或平均分摊。尚未归入任何 loop 的差值累积为 root 的 `unallocated_usage`。这给出“协议观察到的 loop usage”，不是不存在的模型调用级账单。

新增仅向前 MySQL `session_usage_totals` ledger，以 `(app_id, chat_type, chat_id)` 为唯一键，保存累计 `input_tokens`、`output_tokens`、`cached_input_tokens`、`reasoning_output_tokens`、`total_tokens`、`completed_turn_count` 与最后一次 `trace_id/thread_id/turn_id`。`thread_usage_snapshots` 是每个 Thread 的**有效累计高水位**，也是 session total 的唯一计数来源：`turn/completed.usage` 存在时，同一 transaction 先以 `(trace_id, turn_id)` 首次插入 `turn_usage_ledger`，只有 insert 成功才把该 Turn 的权威 raw delta 加入其 Thread 高水位；当前真实 App Server 省略该字段时，以 `thread/tokenUsage/updated.tokenUsage.total` 的完整累计快照替换该 Thread 值，但仅当 input/output/cache/reasoning/total 五个 counter 均不低于当前高水位。随后按同一 chat 的全部 Thread 有效累计值重算 session total。这样 `/new` 后的另一 Thread 快照不会覆盖旧 Thread 的权威用量，乱序/回退快照也不会令总量倒退；快照绝不伪装成 Turn 级权威 ledger。没有任何可用 snapshot 的 Turn 才写 `session_total_incomplete_for_turn=true`。每个 root terminal 都写当前 session total 到 `langfuse.trace.metadata.session_usage_*`。`/new` 更换 Codex Thread 不重置 chat Session total；只有显式的未来“清空会话 usage”命令才可新建累计 epoch，本 Story 不实现该命令。scheduled prompt 以其目标 chat group 累计；scheduled script 没有 chat group 时写 task-run total，不伪造 chat Session。

### 6.3 精确 OTLP 属性与幂等契约

所有 JSON 值以 UTF-8、紧凑 `encoding/json` string 发送；不得把 map 交给 exporter 的默认 attributes 容器。根与每个 child 都携带检索用的 Trace 属性；root input/output **只写 root**，child 只写自身 observation I/O：

| Langfuse 字段 | OTLP attribute | 值 |
|---|---|---|
| Trace 名、用户、会话、tag | `langfuse.trace.name`、`langfuse.user.id`、`langfuse.session.id`、`langfuse.trace.tags` | 固定名、明文 OpenID/session、低基数 string array。 |
| Root/observation input/output | `langfuse.observation.input`、`langfuse.observation.output` | root 同时承担 v4 Trace 表的可见 input/final output；child 只写自身 I/O。根不依赖 `langfuse.trace.input/output` 的 legacy 语义。 |
| Trace 可筛选 metadata（root 与每个 child 传播） | `langfuse.trace.metadata.app_id`、`.task_type`、`.chat_type`、`.workspace_mode`、`.channel_key`、`.chat_id`、`.user_open_id`、`.model_requested`、`.effort_requested`、`.runtime_version`、`.batch_id`、`.scheduled_task_id`、`.source_message_count`、`.source_first_message_id`、`.source_message_ids` | 业务原值逐个扁平化并传播；只因 OTLP 单字段/总 payload 硬上限截断，截断后仍保留明文前缀、`truncated=true` 与原始字节数，不作 hash 或替换。 |
| Root 分层终态 metadata | `langfuse.trace.metadata.codex_status`、`.codex_error_code`、`.request_status`、`.request_error_code`、`.delivery_status`、`.delivery_error_code` | 仅 root，enum/error code 各最大 128 bytes；明确 Codex 与交付终态，供追查/筛选。 |
| Observation 类型、I/O、状态 | `langfuse.observation.type`、`.input`、`.output`、`.level`、`.status_message` | type 只允许 `span|generation|event`；error 同时设置 OTel error status。 |
| Observation 可筛选 metadata | `langfuse.observation.metadata.item_id`、`.turn_id`、`.thread_id`、`.call_id`、`.tool_name`、`.semantic_kind`、`.response_state`、`.unavailable_reason` | 逐个扁平化，不依赖默认 `metadata.attributes`。 |
| loop usage 与原始 Turn/session usage | `langfuse.observation.usage_details`、`langfuse.observation.metadata.raw_usage_*`、`langfuse.trace.metadata.session_usage_*` | loop generation 的 `usage_details` 用互斥桶 `{input,input_cached_tokens,output,output_reasoning_tokens,total}`；所有原始 camelCase counter 另以 metadata 明文保留；Turn 用权威 `turn/completed.usage`，loop 用 snapshot delta，session 用持久 ledger。 |

实现前 P0 必须向新 Project 写一棵最小 Trace（span/generation/event），再通过 Langfuse API/UI read-back 验证上述明文字段、input/output 与 usage 可筛选。操作者必须在目标 Project selector 内确认 nonce、Project ID/name、retention 与 access，并把明文 read-back 证据写入交付记录；Key 的 Project 归属无法通过同一 project-scoped Key 的 API 自证。`agent`、`tool` 不是本 Story 的原生 OTLP type：语义统一由 `semantic_kind` 表达。

为保证 Langfuse Trace ID 等于 canonical 32hex，provider 使用 request-scoped custom OTel `IDGenerator`：它从 root context 取经校验的非零 16-byte TraceID，生成新的非零 SpanID，并启动一个**无 parent 的真实 root span**；child 使用该 root 的真实 SpanContext。无效/全零 TraceID 使该 execution telemetry no-op 并计数，绝不让 provider 自生成不同 TraceID。AT-11/P0 必须断言 root TraceID 等于 canonical ID、root `parentSpanId` 为空且所有 child parent 链正确；不得引入未导出的虚拟 parent。

OTel Span ID 只由 provider 在 observation 创建时生成一次；同一 process 的 exporter retry 发送相同 SpanContext。服务 restart 不重建、不补发已关闭 Trace，活跃 Turn 仍按既有 Runtime 失败语义收尾。Runtime protocol event 去重键为 `(generation, seq)`；同一 key 的 duplicate 不更新 span。这样保证 HTTP timeout/retry 不从业务侧重复创建树，也不承诺不具备的跨进程 backfill。

### 6.4 明文数据与唯一凭据边界

本 Story 的数据策略是：**写入 Langfuse 的所有业务数据均为原文**。没有 `content_mode`、HMAC、hash、digest、`FeishuReferenceClassifier`、业务 Redactor 或“metadata-only”降级路径。下表中的 allow 均是原值，不加密、不替换、不摘要：

| 内容类别 | 写入 Langfuse |
|---|---|
| realtime/scheduled prompt、userMessage、agentMessage、reasoning Item、进展与 final | 完整原文。 |
| 所有 App Server Item、MCP/web、命令、文件变更、动态 `item/tool/call` | 完整参数、progress、结果、错误、response write 原文。 |
| `/goal` objective、approval/elicitation/requestUserInput request/response | 完整原文与真实 ID/nonce。 |
| 附件元数据、下载 URL、本机 session 路径、文件名/key、文档 URL/ID/token、docx/read-document 正文、飞书 API/HTTP 业务 body | 完整原文；二进制附件本体以原始 bytes 的 base64 payload 写入，且有 `content_encoding=base64`、原始 mime/bytes。 |
| OpenID、chat ID、channel key、飞书 event/message ID、task owner、Thread/Turn/Item/call ID | 原值写入 Langfuse 一等字段或 metadata，且允许被搜索。 |

唯一不进入 Trace、attributes、event body、日志、测试失败文本、持久化证据或仓库的是**运行凭据**：Langfuse public/secret key、HTTP `Authorization`/`Proxy-Authorization`、Cookie、飞书 access token、进程环境 secret 与数据库密码。实现用窄的 `CredentialStripper` 仅移除这些运行凭据，不扫描、不改变任何业务文本、身份、文档、附件、路径或 URL；它记录 `credential_stripped_count` 而不记录其值。对 App Server 未提供的隐藏 prompt/reasoning 仍写 `null + unavailable_reason`，因为不存在可存的原文；`reasoning.item` 只表示协议实际发送的 Item，不代表全部内部思考。

为避免单次 exporter request 无界，单字段最大 4 MiB、单 observation 最大 16 MiB、单 exporter batch 最大 64 MiB；超限内容按原始顺序分片为 `*.part.N` observation（带 `part_count`、`original_bytes`、`content_encoding`），同一 Trace 可跨任意多个 batch 导出，不 hash、不压缩为摘要、不丢弃。P0 必须先验证该自托管 Langfuse 版本接受同一 Trace 的多请求分片；若实例存在无法绕过的 Trace 总量上限，则 S08 的“完整明文”验收被阻断，不能截断、摘要或声称完成。只有无法编码的对象才写明确 `serialization_error`，同时保留错误与原始对象类型。

### 6.5 生命周期、并发、两层终态与失败

1. Router 只持久化/传递 message correlation；Channel Worker 在形成 Batch、尚未 `turn/start` 前创建 canonical root TraceContext。重复 receipt 不进入 Batch，不产生第二棵 execution tree。
2. 在 `turn/start` 前的 card-create、output unavailable、pre-turn MySQL 或 Processor preparation 失败由 Channel Worker 直接关闭 root：`codex_status=not_started`、`request_status=failed`、稳定 `request_error_code`；不得遗留未闭合 Trace 或伪称 Codex failure。
3. `codexapp.attempt` 拥有 protocol collector：唯一 stdout reader 在现有同步 dispatch 路径中只做有界内存 append 与 `CredentialStripper`，不调用 exporter、不等待 Worker、不从 raw JSONL 重读。每 Trace 最多 512 Item、64 active spans；高频 delta 仍按 Item 合并，但其完整合并原文必须保留。非终态 observation/event 达到 1,024 时，后续同类原文顺序追加到 `protocol.overflow.part.N` observation（而非 drop）；单字段 4 MiB、单 observation 16 MiB、单 exporter batch 64 MiB，达到上限即顺序分片并继续下一 batch。只有 exporter 故障/队列满才递增 `collector_dropped_<kind>`；仍允许已开 span 关闭、`turn/completed` 与 root/request terminal。它以 `(generation,seq)` 去重，且只接受相同 `thread_id + turn_id` 的 event。unknown/late event 保留原文于 generic/overflow 观测；饱和不关闭 Client、不阻塞路由。
4. `ProtocolTraceObserver` 的并发责任固定为：reader 以 `(generation,thread_id,turn_id,call_id)` 建立 `ToolCallState{request_received,handler_terminal,response_write_terminal,handler_start_permit}`；工具 handler goroutine只推进 handler success/error；唯一 stdout writer 返回后只推进 response-write success/failure。每个转换 CAS 幂等。`item/tool/call` 的 `TryRegisterToolCall` 与 Protocol terminal 的 `CloseProtocolAndDrain` 共享同一 attempt 级 mutex（或等价的单一可线性化 CAS 状态）：前者在**同一临界区**检查 `protocol_terminal_fence`、登记 state、取得 handler-start permit；后者在该临界区先关闭 fence、原子快照并 terminalize 所有已登记 state。因而不存在“terminal 已遍历、reader 才插入 state”的缝隙。handler 在真实副作用前必须 CAS 消耗 permit 并再次确认 state 未被 terminalize；terminal 抢先时已登记但尚未执行的 handler 不得产生副作用，只写稳定协议 error response。fence 已关闭时不得创建 observation/state、不得执行 handler，只计 `late_tool_request_after_protocol_terminal` 并写相同的协议正确稳定 error response。schema-invalid、unmatched 与 unsupported server request 也必须以 call-id/可得 route 记录 terminal；observer 反向不得阻塞 reader/writer。
5. **Protocol terminal**只属于 `codex.turn`：`turn/completed`、interrupt deadline、App Server generation exit 由 Runtime terminal owner first-wins，调用上述 `CloseProtocolAndDrain`，再关闭未完成 Item/loop span，并写 `codex_status`、`codex_error_code`。之后到达的 handler/write 只增 `late_tool_result_count`，绝不更新已经闭合的树；writer response error 必须通过 call-id 回报 observer。若 `turn/completed.usage` 存在，以 `usage_source=turn_completed`、`aggregation_scope=turn` 原样写 root metadata，并经首次 `turn_usage_ledger` insert 事务性写入 Thread 有效累计值；否则若已经收到 `thread/tokenUsage/updated.tokenUsage.total`，仅在五个 counter 全部不低于该 Thread 已存高水位时替换其值。两种路径都由全部 Thread 的有效累计值重算 session total，标记相应 source；两者均不存在时才标记 `session_total_incomplete_for_turn=true`。前者不得伪装为单个 `agentMessage` 的 usage/cost，后者只能按 §6.2 的 snapshot 规则归属到 loop。该动作不结束 root Trace。
6. **Request terminal**只属于 Channel Worker：在 protocol result 之后完成 card create/stream update/terminal update 或 fallback text、scheduled outbox suppressed/rejected/unknown 与 MySQL 条件状态更新；这些写 root 的 `delivery_status`/`delivery_error_code`，再关闭 root 与 `delivery.feishu`。因此“Codex completed 但飞书 delivery failed”是 `codex_status=completed`、`request_status=failed`，绝不伪造成 Codex 失败。
7. cancel 与 `turn/completed` 竞争时按既有 terminal arbiter 的第一原因固定 Protocol terminal；Worker 只读取该不可变结果再做 delivery。root 关闭后到达 event 一律不改结果，只增本机 `late_event_count`，不得阻塞 stdout reader。
8. exporter 初始化失败、HTTP 401/5xx、超时、队列满、panic 或 Shutdown flush 卡住均只产生 `observability_*` 脱敏计数日志；业务 goroutine 不等待 2s export timeout。禁用配置使用 no-op recorder，`observability=degraded` 不是主服务 readiness gate。

### 6.6 配置与包边界

新增 `observability` 配置（名称以现有 config 样式最终落定）：

```yaml
observability:
  langfuse:
    enabled: true
    base_url: https://existing-self-hosted-langfuse.example
    # 或只在本机环境提供：base_url_env: LANGFUSE_BASE_URL
    public_key_env: LANGFUSE_PUBLIC_KEY
    secret_key_env: LANGFUSE_SECRET_KEY
    project_id: codex-workspace-bot-project-id # human binding evidence
    # 只有在目标新 Project selector 内完成 nonce write/read-back 后填写；
    # project_binding_verified=false 时 exporter 保持 no-op。
    project_binding_nonce: "S08-P0-..."
    project_binding_verified: true
    environment: development
    export_timeout: 2s
    max_queue_size: 4096
```

`base_url` 与 nonsecret `project_id` 可出现在模板；key 只能由环境或 gitignored 本机配置提供。启动时若 `enabled=true` 但任一 Key 无效，服务以 no-op telemetry 启动并记录一次 `observability_config_invalid`，不阻断飞书 ingress。所有成功的 exporter 都写入明文业务数据；不存在 content-mode、identifier salt、fingerprint 或自动降级。首次启用、key rotation、project 变更都必须在目标 Project selector 做人工 nonce/read-back 绑定并作为发布门。健康/readiness 要显示 `observability=disabled|ready|degraded`，但其 degraded 不是主服务不可用。

包边界：`internal/observability` 仅拥有明文 projection、唯一 `CredentialStripper`、root/protocol recorder、usage attribution 与 OTLP provider；`worker` 创建/关闭 root request Trace；`codexapp` 的 attempt/client 通过 `ProtocolTraceObserver` 提供已分类 event、usage snapshot 和 tool handler/result/write lifecycle；`router`/`schedule` 只构造 ingress metadata；`storage` 负责 scheduled run trace ID migration/claim 以及 turn/session usage ledger；`cmd/server` 负责 provider 生命周期与有限时间 flush。禁止让 Langfuse SDK/OTel 类型泄漏到 Router、Storage 或 Feishu 包。

## 7. 测试设计与验收标准

| 编号 | Given / When / Then |
|---|---|
| S08-AT-01 | Given realtime Feishu event `app_id,event_id`, When receipt 成功，Then `storage.TraceID` 是非零 32 hex；重复 receipt 不进入 Batch，绝不创建第二棵 execution tree。 |
| S08-AT-02 | Given同频道两条 realtime Message 被合为一个 Batch、另一频道并发，When Processor 只发一次 `turn/start`，Then canonical Trace ID 等于首条 Message Trace ID，只有一棵 Item/tool/delivery tree；第二条以其真实 message ID、position 与原文作 source correlation，二者不重复 token/final；另一频道 Trace 完全隔离。覆盖 race 和 late event。 |
| S08-AT-03 | Given claimed prompt/script run、进程 restart 与未来人工 retry，When claim/execute，Then migration 后 `ClaimedRun.TraceID` 在首次 claim 以 crypto/rand 非零 32hex 条件写入并传递给 PromptDispatcher/script executor；restart 标记 `unknown_interrupted` 而不重放/补写，人工 retry 只能新 run/new trace。 |
| S08-AT-04 | Given same App/chat 连续两 canonical turn、另一 App 使用相同 chat ID，When export，Then前两条共享明文 `app_id:chat_type:chat_id` Session ID、时间顺序正确；另一 App Session 不同；OpenID/chat ID/channel key 和飞书 event/message ID 以原值出现在规定 OTLP 属性中并可直接筛选。 |
| S08-AT-05 | Given S03 实测 fixture（reasoning text/summary、commandExecution、fileChange、MCP/web/remote-control payload、unknown Item、agent message delta、token usage、terminal），When Runtime 路由，Then树有正确 loop/item 父子关系；556 delta 合为一个 item output；任何含业务 payload 的 MCP/remote-control/未知 Item 被 generic/overflow span/event 以完整原文表达而非丢弃。 |
| S08-AT-06 | Given `item/tool/call`、handler success/error、response write success/failure、command outputDelta，When event sequence含乱序和重复，或以可控 barrier 令 terminal 与 `TryRegisterToolCall` 的 state 写入/handler 启动许可竞争，或 protocol terminal 先到再到达新的 tool call，Then同一线性化临界区使 `(generation,thread,turn,call)` 的状态机恰好一次收尾；terminal 获胜时不得遗留未被 drain 的 state，已登记但未获 permit 的 handler 不执行副作用，terminal 后的新 tool call 不建 state/span、不执行 handler、仅计 `late_tool_request_after_protocol_terminal` 并获稳定协议 error response；terminal 后 handler/write 只计数，writer error 有 call-id，绝不更新已结束树。 |
| S08-AT-07 | Given card-create、output unavailable、pre-turn MySQL 失败，或 `turn/completed` 与 cancel/timeout/App Server exit 竞争、随后 delivery 成功/拒绝/未知/失败，When终态到达，Then未 start 的 root 是 `codex_status=not_started`；已 start 的 `codex.turn` 与 root request 分层关闭；Codex completed + delivery failed 保持两个不同 status/error，不丢失 Worker/DB 收尾。 |
| S08-AT-08 | Given App Server 没有返回隐藏 prompt 或完整 reasoning，When generation/loop 完成，Then相应 input/reasoning 是 `null` 且有稳定 unavailable reason；绝不把 Thread snapshot 猜成 model-call 或 generation usage。 |
| S08-AT-09 | Given Goal/approval/doc-read/attachment/API-body、`doc_create_and_announce`/`doc_read`/`file_upload_and_send_current_channel`/`message_send_current_channel` 参数与结果、用户输入 doc URL、嵌套 JSON、URL query、OpenID/chat/document/file/message values、含业务参数/结果的 MCP/remote-control payload、binary 与超限 payload，When record/fuzz，Then所有业务原值在 OTLP body/attributes 中可读可搜，binary 以有明确 encoding 的完整分片可重组；仅 Langfuse/飞书运行凭据、Authorization/Cookie 与环境 secret 在 OTLP body/attributes、headers、slog 和测试输出中零命中。 |
| S08-AT-10 | Given `turn/completed.usage` 与多次 `thread/tokenUsage/updated` 覆盖多个 loop（至少一个 loop 内 S0→S1→S2 三个递增 snapshot）、loop 外间隙、重复通知、reconnect/restart、缺失 completed usage 的 Turn，以及 `/new` 后同 chat 的新 Thread，When terminalize，Then每有 completed usage 的 Turn 的原始 input/output/cached/reasoning/total 以权威 completed usage 一次写入；当 `total=input+output` 且 cached/reasoning 为包含关系时，Langfuse generation usage 以互斥 `input`/`input_cached_tokens`/`output`/`output_reasoning_tokens`/`total` 写入，否则只保留原始 counter + unavailable reason；该 loop 的 usage 等于所有去重 snapshot delta 的和，跨 loop/Turn delta 不进入任何 loop 且记 `unallocated_usage`；只有 `(trace_id,turn_id)` 首次 ledger insert 才写 Turn ledger。session 使用按 Thread 保存的有效累计高水位：权威 Turn delta 加到其 Thread；缺失字段的 `tokenUsage.total` 只有五个 counter 均不小于既存高水位时才能替换该 Thread；session 始终汇总全部 Thread。因此新 Thread 的 snapshot 不覆盖旧 Thread 的权威用量，乱序/旧 snapshot 不令计数回退。只有两种数据皆缺失才标记 `session_total_incomplete_for_turn=true`，绝不平均分摊或重复相加。 |
| S08-AT-11 | Given 1,025th generic/tool/hook observation、513th Item、65th active span、16MiB observation、64MiB exporter batch、超过 64MiB 的单一 Trace，或 disabled、错误 Key、401、5xx、2s timeout、队列满、exporter panic 或 Shutdown flush 卡住，When worker 处理真实/fake turn，Then所有入口先 reserve；collector 以 overflow/跨 batch 顺序分片保留业务原文而非摘要/drop，且分片可完整重组、仍保留 terminal；只有 exporter 不可用时允许 fail-open 丢失 telemetry 并计数；stdout reader 与业务 goroutine 不等待 exporter；飞书交付、MySQL terminal、Worker 释放与现有错误码不变。 |
| S08-AT-12 | Given fake OTLP/HTTP collector, When a completed turn flushes，Then隔离 collector 用内存测试 pair 验证 Basic header 解码值但不输出其值；请求路径、ingestion version、canonical 32-hex TraceID、**无 parent 的 root**与 root/child parent chain、完整 source metadata、明文业务 input/output/ID、usage details，以及 §6.3 每个精确 `langfuse.trace.*` / `langfuse.observation.*` attribute、JSON encoding 均正确；同 process HTTP retry 使用同一 SpanContext，不跨进程补发。 |
| S08-AT-13 | Given新 Project 的本机 Key 与新启动 bot, When P0 span/generation/event、明文 nonce/read-back 后发送唯一 realtime、batched realtime、scheduled prompt 和 script，Then人工绑定通过后 exporter 写入明文业务数据；Project 内可按真实 session/user/app/task type 检索，旧 CC Project 无新增，且记录实例版本/access/retention/revoke 证据。 |

L1：明文 ID/session、scheduled trace migration/claim、credential stripper、分片、event→observation mapper、loop usage attribution、turn/session usage ledger。

L2：fake App Server/Feishu/OTLP exporter、two-message batch canonicality、Router→Worker→Runtime/ToolLifecycleObserver 的关联、protocol/request terminal race、disabled/fail-open。

L3：真实 `codex app-server` 与 fake OTLP collector，然后新自托管 Langfuse Project 的 P0 type/attribute read-back 和唯一 nonce 写入/read-back。

本 Story 不改变飞书用户可见协议，因此不以 L4 作为 Langfuse 本体准出门；若实现意外改变飞书流式/终态，须追加对应 L4。

## 8. 最终本地集成校验

**S08-LI-01：新 Project 的实时与定时 Trace 可检索性**

前置：新二进制/新进程、当前 MySQL migration（含 `scheduled_task_runs.trace_id`、turn/session usage ledger）、`/healthz=ok`、receiver connected、Codex 已登录；新 Langfuse Project 与新 Key 已按 §4.1 配好，`observability=ready`，并记录 Project 名/ID、实例版本、access 成员、retention 和 Key revoke 路径（不记录 key）。

1. 先在目标 Project selector 内执行 P0 span/generation/event 的 OTLP write/read-back，确认 Project 能按原始 `app_id/task_type/session/user/OpenID/chat ID` 筛选、明文 I/O 和 usage 显示正确。该步骤是明文写入发布门，不以同 Key API 自动证明 Project 归属。
2. 在同一测试 p2p 连续发送两条唯一文本 `S08-BATCH-A/B-<nonce>` 使其合批；随后创建或触发一个 scheduled prompt 和一个 scheduled script，二者均带相同 nonce。
3. 等待 Worker/script terminal；保存本机 source message/run ID、canonical 32-hex trace ID、明文 session ID、thread/turn ID、batch ID、终态与新进程 PID/启动时间，以及 Turn/loop/session input/output/cached/reasoning/total token 值。确认第二条合批 message 没有复制的 execution Trace。
4. 进入**新 Project**，分别按 canonical trace ID、真实 OpenID/Chat ID 与 `task_type` 查找；确认 batch、prompt、script 各一条、同频道 session 顺序正确，展开后能看到 root input、可见进展/final、工具/command、完整 MCP/remote-control 业务 payload 和分层 terminal；每个可归属 loop 与 session snapshot 显示正确 usage。
5. 使用 API 或 UI 导出该 Trace 的 JSON；确认原始 OpenID、chat ID、Goal objective、approval nonce、附件路径、文档正文、工具文本与 MCP/remote-control payload 均可读；以超过 64MiB 的本机 fixture 验证同一 Trace 跨 batch 分片可完整重组；同时搜索 Langfuse/飞书 Key、Authorization、Cookie、进程环境 secret，必须为零命中。确认同一时间窗口旧 CC Project 无新增，且新 Key 只可见目标 Project。
6. 临时以错误 Key 重启并发送另一个唯一请求；确认用户可见流程与 DB 终态仍成功/合法失败，readiness 报 `observability=degraded`，本机有一次凭据隔离失败计数。恢复 Key 后以**新进程**重复步骤 1–5。

失败时先检查：新进程是否使用新配置、Project Key 是否属于目标 Project、OTLP 路径/HTTP status、`trace_id` 格式、Runtime route、usage ledger/attribution 与 observer dropped/error 计数。不得以 fake collector 或旧 Project 的历史数据替代本项。

### 8.1 2026-07-14 已完成的本机外部验收

- 已在既有自托管 Langfuse Organization 中创建隔离 Project `codex-workspace-bot-s08`（Project ID `cmrkb46en0003p9072prnpp5d`），并创建仅供本 Bot 使用的一对项目级 API key；key 仅保存在 gitignored 本机 `.env`，不记录到本文档、仓库或交付证据。
- 新进程的 `/healthz=ok`，`/readyz` 显示 `observability=ready` 且四个飞书 receiver connected。
- 以 canonical trace ID `a08f1e0a08f1e0a08f1e0a08f1e0a08f` 写入 P0 Trace `langfuse.project.binding.p0`，并在**新 Project**的 Trace UI read-back 到原始 binding nonce、明文 input、明文 output、Project ID、session/user 和 metadata。
- 同一 Project UI 随后已读回重启后的真实 realtime 飞书 Trace，包含真实 chat/user/app/Thread/Turn correlation metadata；本机 MySQL 已同时保存该请求终态与 thread/session usage 快照。当前 App Server 省略 `turn/completed.usage`，因此该样本使用 `thread/tokenUsage/updated.tokenUsage.total` 回退，符合 §6.2。
- 未执行的 LI-01 项保持显式：合批、scheduled prompt/script、64MiB payload、错误 key/网络/队列/panic 注入与旧 Project 零写入时间窗比对仍须作为后续独立本机验收，不能由上述 realtime 证据替代。

## 9. Definition of Done

- [ ] 新 Project/key 已建立并通过预检；旧 CC Workspace Bot Project/key/data 未改变，key 从未写入版本库或交付证据。
- [ ] 一个 Batch/Turn 只生成一棵 canonical execution Trace；source message correlation、合批/并发/late-event race 有 L2 证据，且不重复 token/tool/final。
- [ ] realtime、scheduled prompt、scheduled script 均使用持久化 32 位 Trace ID；Trace ↔ MySQL message/run ↔ App Server thread/turn 可交叉关联。
- [ ] 明文 Session/User/metadata、低基数 tag、明文 prompt/output/reasoning/tool/附件/文档/业务 ID 和仅运行凭据隔离，有 L1/L2 证据。
- [ ] 全量 Item fallback、dynamic tool 的 request/handler-result/response-write、protocol/request 两层 terminal、Turn/loop/session input/output/cached/reasoning/total token 语义映射为稳定树；隐藏 prompt/reasoning 与无法归属 loop usage 均明确标注为不可得/未归属而非伪造。
- [ ] `turn_usage_ledger` 与 `session_usage_totals` migration/幂等累计、§6.3 的无 parent custom-ID root、OTLP 精确属性、P0 Project-selector 明文 read-back、错误 Key/网络/队列满/panic/flush fail-open、terminal race 与同-process transport retry有测试；运行 `gofmt`、`go vet ./...`、`go test ./... -count=1` 与相关 `go test -race`。
- [ ] S08-LI-01 在新进程、新 Key、新 Project、真实 MySQL 和真实 Codex App Server 上完成；证据保存明文 Trace/Session/用户与会话 ID、thread/turn、各层 usage、HTTP health、DB terminal 和凭据零命中查询结果。
- [ ] 在启用任何 Langfuse 配置**之前**，`config.yaml.template`、`.env.example`、README、AGENTS、HLD 可观测性章节、Story List 和运行手册已同步并经独立 review；明确 Langfuse 是 fail-open 观察面，不是消息处理真相源，且新自托管 Project 写入明文业务数据。
- [ ] 独立技术架构、质量/隐私、产品可观测性评审均无 blocker；若有 blocker，整改后以新 reviewer 复审。

## 10. 风险、残留项与后续 Story

| 风险/残留 | 当前控制 | 后续责任 |
|---|---|---|
| 明文 Trace 含完整业务数据 | 用户明确要求个人自用的新自托管 Project 全文可读；实施前同步 AGENTS/HLD，并由操作者确认 Project access/retention/备份边界；运行凭据仍永不导出 | 数据留存/访问治理 Story。 |
| App Server schema 漂移 | 当前 CLI 生成 schema、S03 fixture 与 L3 captured events 作为兼容门；未知 event 不跨 Trace 路由 | Protocol compatibility SOP。 |
| App Server 未暴露底层 prompt/CoT/调用级 cost 或每 loop 的权威调用 usage | `null + unavailable_reason`；Turn 记录权威 `turn/completed.usage`，loop 只记录观察到的 `thread/tokenUsage` snapshot delta 与未归属部分，不误报为精确账单 | 若 Codex 将来提供权威 loop/model-call telemetry，单独 Story 扩展 mapper。 |
| exporter 丢失数据 | 有界 fail-open 队列与 dropped/error 计数；不影响业务，不跨进程补发 closed Trace | 未来 Collector/持久 outbox 需独立评估，不能悄然引入。 |
| Langfuse Project 命名/retention | 推荐默认值已给出，最终由实例操作者在建 Project 时决定 | 将选择记录到 S08 Delivered 证据。 |

### 设计依据

- [Langfuse：什么是好的 Trace](https://langfuse.com/docs/observability/best-practices)
- [Langfuse：OpenTelemetry 接入与属性映射](https://langfuse.com/integrations/native/opentelemetry)
- [Langfuse：Go 等语言经 OpenTelemetry 接入](https://langfuse.com/resources/engineering/opentelemetry-languages)
- [Langfuse：自托管初始化、Organization/Project/Key 关系](https://langfuse.com/self-hosting/administration/headless-initialization)
- [Codex App Server：Thread、Turn、Item 与 JSON-RPC](https://learn.chatgpt.com/docs/app-server.md)
- [S03 实测 App Server Event 时间线](S03-真实请求Event时间线与展示决策依据-2026-07-12.md)
- [Story 从设计到 Delivered](../sop/story-design-to-delivery.md)
