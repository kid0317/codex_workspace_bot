# S09：跨应用对话历史与 Langfuse 查询工具

> **状态**：Draft（待操作者讨论与确认）  
> **关联概设**：[概要设计 v4.5](../02-redesign-high-level.md) §2、§5、§9、§11  
> **依赖**：S01–S08；本机 MySQL；已绑定的自托管 Langfuse Project；已登录的本机 Codex App Server；测试飞书 App。

## 1. 一句话目标

让任一已启用飞书 App 中的 Codex Agent 可以发现全部本地 App/会话，并按任意应用、会话、时间、文本或 Trace 关联查询完整 MySQL 对话历史与当前 Langfuse Project 的完整执行 Trace，从而自行回溯跨应用工作。

## 2. 背景

旧工作区已有两个可参考的本地 Skill：`chat_history` 以当前频道为安全边界，从 SQLite 的 `sessions/messages` 查询最近消息；`langfuse_query` 通过 Langfuse REST API 提供最近 Trace、异常、单 Trace observations 和 session 查询。两者都证明了“先找会话、再查历史、最后展开 Trace”的排障路径有价值，但均不适合本项目：本 Bot 的事实来源是 MySQL，且本 Story 的明确产品要求是任意 App 可查询任意 App，不能从当前调用频道推导或强制查询范围。

当前 MySQL 的稳定关系是 `apps → chat_groups → messages`。一条 `messages` 行持有用户正文、最终助手正文、状态、飞书标识和 canonical 32-hex `trace_id`；`chat_groups.codex_thread_id` 只表示**当前**可续接 Thread，不是历史每轮的 Thread 记录。S08 已把相同 `trace_id` 写入当前自托管 Langfuse Project，并以 `app_id:chat_type:chat_id` 作为 Langfuse Session ID。

2026-07-15 的本机只读验证已确认当前 Langfuse v3 Project 可用：`GET /api/public/traces`、`GET /api/public/traces/{id}`、`GET /api/public/observations?traceId=...` 和 `GET /api/public/sessions/{id}` 均成功，并返回完整 input/output、metadata、session/user 和 Trace/observation 关联字段。当前 MySQL 有 5 个 App、10 个 ChatGroup、234 条消息；尚无支撑全局或 ChatGroup 时间线 keyset 查询的专用索引。

## 3. 范围

### 3.1 本期做什么

1. 将 Thread Dynamic Tool catalog 扩展为三个只读 namespace/tool：
   - `workspace.list_query_targets`：发现全部 App 及其可查询 ChatGroup；
   - `history.search`：从 MySQL 查询跨 App 对话轮次；
   - `langfuse.query`：查询已绑定的当前 Langfuse Project。
2. 所有三个工具均允许从任意调用 App 查询任意已登记 App、ChatGroup、发送者、时间段和 Trace；调用者的 App、频道、Actor、workspace 或 Reply Target 不作为查询授权条件，也不作为隐式 SQL/Langfuse filter。
3. 返回业务数据原文：App/ChatGroup/消息/Trace/Observation 的业务字段、真实 ID、正文、路径、URL 和 metadata 均不脱敏、不摘要、不截断。唯一不返回的内容是运行凭据，例如 Langfuse Key、Feishu Secret、Authorization/Cookie、token、数据库密码和进程环境 secret。
4. 以 `trace_id` 作为 MySQL 历史与 Langfuse Trace 的稳定跨系统关联键；目录工具返回 `langfuse_session_id` 和 ChatGroup 的**当前** Thread ID，明确不伪造历史 Thread 关联。
5. 为 MySQL 历史检索增加仅向前 migration：`messages(chat_group_id, created_at, id)`、`messages(created_at, id)`、`messages(sender_open_id, created_at, id)` 索引。查询使用参数化 SQL 与 keyset cursor，支持无服务端硬上限的逐页完整遍历。
6. 把现有二元的 `FeishuDynamicTools/S06DynamicTools` 选择改成一个显式的完整 catalog provider；它按实际启用功能生成 version 和 tools，所有普通 Turn 与 `/goal` 都先走同一持久 catalog upgrade，再 `thread/start`。S09 的目录必须包含启用时的既有 `feishu`、可选 `schedule` 与三个新查询工具。
7. 新建 `internal/queryaction`，使 MySQL 目录/历史查询、Langfuse REST client 和 App Server ToolResult 适配保持独立；`cmd/server` 只负责构造服务、配置和按 namespace 分发。
8. 新增 `query_tools` 配置。启用查询工具时，Langfuse 的 base URL、public key 和 secret key 必须可用；凭据解析与 S08 exporter 是否启用解耦。因为 Langfuse 查询是用户主动请求的功能，配置缺失应在启动时明确失败，而不是在对话中静默降级。

### 3.2 本期不做什么

- 不查询任意 Langfuse Organization/Project：本期只查询配置中已绑定的**当前 Project**。如需跨 Project 查询，必须另起 Story 设计多 Project 凭据与发现模型。
- 不新增历史 Thread ID 回填或猜测：旧 `messages` 未保存 Thread ID，S09 只提供真实的当前 ChatGroup Thread 和 Trace 关联；历史 Thread 物化是独立的数据演进。
- 不新增聊天 UI、slash command、Web 页面、全文搜索服务、向量检索、导出文件、异步报表或缓存层；Agent 通过 Dynamic Tool 自行编排查询与解释。
- 不写入、删除、修改 MySQL 业务数据或 Langfuse 数据；本 Story 的工具均为只读，因此不复用 S05/S06 的副作用 replay ledger。
- 不向 Codex 暴露运行凭据、数据库 DSN、Feishu App Secret 或任意 HTTP Authorization/Cookie；“不脱敏业务数据”不改变这一既有运行凭据边界。
- 不以 `page_size`、调用者身份、默认最近 N 天或默认 App 作为产品范围限制。分页只解决 App Server/HTTP 传输与 Agent 上下文的分批读取，Agent 可继续请求直到所有匹配数据被读取。

## 4. 依赖与前置条件

| 依赖 | 当前状态 | S09 如何使用 |
|---|---|---|
| S01 MySQL `apps/chat_groups/messages` | Delivered | MySQL 为 App 目录和对话历史唯一事实来源；`messages.trace_id` 是 Trace bridge。 |
| S03 App Server Dynamic Tool server-request 路由 | Delivered | `thread/start.dynamicTools` 注册工具；Runtime 继续以 exact `thread_id + turn_id + call_id` 绑定每次调用并返回一次协议正确结果。 |
| S05 Dynamic Tool catalog / Feishu 工具 | Delivered | 复用已有 catalog archive/start 和 server handler 分发模式；不复用副作用 ledger。 |
| S06 catalog 持久状态机 | Delivered | 所有 catalog 变化经 `archive_pending → start_pending → stable`，旧 Thread 不得 resume 后补工具。 |
| S07 `/goal` | Delivered | 修正 Goal start 路径，使其使用同一完整 catalog provider。 |
| S08 自托管 Langfuse Project 与 Project Key | 已有本机 binding/read-back | `langfuse.query` 使用同一 Project 的 REST API；不直连 ClickHouse/PostgreSQL。 |
| 本机 MySQL / Langfuse / Codex / 测试飞书 App | 已运行 | L2/L3/L4 真实集成校验的外部边界。 |

## 5. 核心设计决策

| 问题 | 结论 | 原因 | 后果 |
|---|---|---|---|
| 工具形态 | 使用 `workspace`、`history`、`langfuse` 三个单一职责工具。 | 查询前必须发现 App/会话；MySQL 与 Langfuse 有不同 cursor、错误和详情模型。 | Agent 先目录、再历史/Trace；不采用一个带大量 mode 的“万能查询”工具。 |
| 与旧 Skill 的关系 | 保留旧 Skill 的查询语义，但迁移到 Bot-owned Dynamic Tool。 | 旧 `chat_history` 的频道隔离与 SQLite 结构不再成立；旧 Langfuse 的四种工作流仍适用。 | `langfuse.query` 保留 traces、observations、session、errors，另补单 trace detail。 |
| 跨 App 策略 | selector 完全由工具 arguments 决定，route 只证明该调用属于当前 Turn。 | 操作者已明确个人自用，任意 App 可查任意 App。 | 不推导 owner / current channel filter，不做 AllowedApps/AllowedChats/ID hash。 |
| 目录粒度 | 返回 App 与 App 下 ChatGroup 两级目录。 | App 名不足以选择具体会话；ChatGroup 能提供 MySQL selector、当前 Thread 与 Langfuse Session ID。 | 返回 `chat_group_id/chat_type/chat_id/current_thread_id/langfuse_session_id`，但不返回 App Secret。 |
| 历史关联 | MySQL ↔ Langfuse 以 `trace_id` 为主键关联。 | 每轮 message 已持久化 canonical Trace ID；历史 Thread ID 未持久化。 | `history.search` 接受 `trace_ids`；目录的 Thread ID 仅标注 `current`。 |
| 原文与凭据 | 业务 payload 原样返回，运行凭据永不进入输出或日志。 | 符合个人自用的明文业务数据裁决，同时保留不可泄漏的 credential 边界。 | 不调用业务 Redactor；HTTP Client 禁止把 auth header 放入 error/ToolResult。 |
| 分页 | 使用稳定 keyset cursor；没有最大页数、时间窗或 App 数限制。 | 大结果需要分批传输，但产品不能因调用者身份或范围被截断。 | 默认页仅是便利值；不设置 server-side maximum，cursor 可一直推进。 |
| Langfuse 数据源 | 使用 Project-scoped public REST API，不直接查询 ClickHouse。 | 旧 Skill 与实际运行实例均已证实 API 可返回 Trace/Observation/Session；避免耦合 Langfuse 内部表。 | 版本兼容性以 L3 captured response 及 client contract test 守护。 |
| 副作用与重放 | 不建 query tool-call ledger。 | 查询没有业务副作用；重复请求最多得到更新后的读快照。 | Runtime exact attempt binding 仍是强制前置；不声称跨请求快照一致性。 |
| catalog 版本 | 由完整 catalog provider 生成版本，而非硬编码 `S06ToolCatalogVersion` 分支。 | Schedule/Query 可独立启用，且 `/goal` 目前绕开完整 catalog。 | 任一实际 schema/tool 集合变化都生成新 version，并强制 archive/start。 |

## 6. 主链路与数据/接口契约

### 6.1 完整 catalog 与分发

启动时 `cmd/server` 根据 `feishu_actions.enabled`、`schedule.enabled` 与 `query_tools.enabled` 构造不可变 `ToolCatalog{Version, Tools}`。Version 必须由**完整**工具集合与 Schema 决定，例如 `s09-query-feishu-v1`、`s09-query-schedule-v1`；不得让相同 version 对应两套不同 schema。

`Processor.Process` 与 `Processor.processGoal` 都必须先以该 `ToolCatalog` 调用现有持久升级状态机。只有 `stable` 且 persisted version 等于目标时才 `thread/resume`；否则 archive 旧 Thread、持久化新 Thread 后才开始 Turn/Goal。所有 `thread/start` 传同一完整 `dynamicTools`。这使查询工具与 S05/S06 一样遵守 exact attempt binding，却不把当前 route 变成查询权限边界。

`cmd/server` 的 `ToolHandlers` 仍从 `worker.Batch` 构造 Feishu/Schedule route；新增的 `queryaction.Service.Execute(ctx, call)` 不读取这些 route 身份作 selector。未知 namespace/tool 返回稳定 `tool is unavailable`。Runtime 不接受未匹配的 `thread/turn/call`，并且每个 server request 仍只写一次 JSON-RPC response。

### 6.2 `workspace.list_query_targets`

Schema 是空 object（`additionalProperties:false`）。一次调用返回所有 App，按 `name,id` 稳定排序；每 App 返回：

```json
{
  "app_id": "...",
  "name": "...",
  "enabled": true,
  "workspace_dir": "/absolute/path",
  "workspace_mode": "work",
  "model": "...",
  "reasoning_effort": "...",
  "chat_groups": [{
    "chat_group_id": "...",
    "chat_type": "p2p|group",
    "chat_id": "...",
    "current_codex_thread_id": "...|null",
    "catalog_version": "...",
    "catalog_upgrade_state": "stable|archive_pending|start_pending",
    "last_message_at": "RFC3339|null",
    "message_count": 0,
    "first_message_at": "RFC3339|null",
    "langfuse_session_id": "<app_id>:<chat_type>:<chat_id>"
  }]
}
```

App credentials（`feishu_app_secret`）及任何运行 token 不参与 SELECT 或 JSON marshal。所有其他字段均是原值；目录不得根据当前调用 App、ChatGroup 或 Actor 过滤。

### 6.3 `history.search`

输入为严格 JSON object；数组内是 OR，已出现的不同 selector 字段之间为 AND。所有 selector 均可省略，完全省略时匹配全部历史：

```json
{
  "app_ids": ["..."],
  "app_names": ["..."],
  "chat_group_ids": ["..."],
  "chat_types": ["p2p", "group"],
  "chat_ids": ["..."],
  "trace_ids": ["32-hex"],
  "sender_open_ids": ["..."],
  "statuses": ["received", "processing", "succeeded", "failed"],
  "keyword": "literal text",
  "from": "RFC3339|null",
  "to": "RFC3339|null",
  "order": "asc|desc",
  "page_size": 100,
  "cursor": "opaque|null"
}
```

`keyword` 对 `user_content` 与 `assistant_content` 做参数化 literal `LIKE` 匹配；本期不宣称中文分词、相关性排序或语义搜索。查询按 `(created_at,id)` keyset 排序，cursor 同时绑定排序方向与最后一行键值；selector 或 cursor 无效返回 `history_invalid_query` / `history_invalid_cursor`。`page_size` 必须为正整数，但没有服务端最大值；默认值只控制单个 ToolResult 的便利大小。

每条原样返回 App/ChatGroup 标识、`message_id`、`trace_id`、飞书 event/user/bot message ID、`sender_open_id`、`user_content`、`assistant_content`、状态、错误码、耗时、receipt/completed 时间。结果包含 `next_cursor`，为空表示已无更多行。`current_codex_thread_id` 可随 ChatGroup 一并返回，但不得命名为或暗示为此历史行的 Thread ID。

Migration `010_s09_query_indexes.sql` 在现有数据上建立：

```sql
KEY idx_messages_group_created (chat_group_id, created_at, id),
KEY idx_messages_created (created_at, id),
KEY idx_messages_sender_created (sender_open_id, created_at, id)
```

Migration 必须沿用当前校验和与 forward-only 机制；若本机已有等价索引，使用 `information_schema` 条件 DDL 避免启动失败。

### 6.4 `langfuse.query`

工具不会把 base URL、Key 或 Basic Authorization 交给 App Server。它使用配置加载后的 Project-scoped client，所有成功响应的**业务** JSON 则原样嵌入 `inputText`。输入 `mode` 必填：

| mode | 必填参数 | 行为 |
|---|---|---|
| `traces` | 无 | 透传可选 `from_timestamp`、`to_timestamp`、`session_id`、`user_id`、`name`、`environment`、`tags`、`page_size`、`cursor`；可选 `app_ids` 在 client 端按原始 `metadata.app_id` 筛选，且继续读取上游页直到当前结果页满或源耗尽。 |
| `trace` | `trace_id` | 调用 `/api/public/traces/{trace_id}`，返回原始 Trace。 |
| `observations` | `trace_id` | 调用 `/api/public/observations?traceId=...`，返回原始 Observation 页。 |
| `session` | `session_id` | 调用 `/api/public/sessions/{session_id}`，返回该 Session 原始详情与 Trace 关联。 |
| `errors` | 无 | 以 `traces` 的时间/metadata selector 扫描 Trace；对 error/warning Trace 或包含 error/warning/statusMessage Observation 的 Trace，返回原始 Trace 及匹配 Observation。 |

`traces` 和 `errors` 的可选 `app_ids` 使用目录返回的内部 App ID；它是内容 filter 而非访问控制。`session_id` 直接接受目录返回的 `langfuse_session_id`。除 mode 特有字段外，schema 均 `additionalProperties:false`；未知字段、空必填字段、坏 cursor 和错误 timestamp 返回 `langfuse_invalid_query`。

Client 将上游 `data` 与 `meta` 原样保留，并把上游分页状态封装进本工具的 opaque cursor。它不因为跨 App、时间范围或结果数量停止扫描；HTTP context timeout、取消、非 2xx、无法解码或上游分页循环分别返回稳定 `langfuse_query_timeout`、`langfuse_query_cancelled`、`langfuse_query_http_failed`、`langfuse_query_decode_failed`、`langfuse_query_pagination_failed`。错误内容不得含 Key/Header/Cookie；业务响应正文不作脱敏。

### 6.5 配置与包边界

新增配置：

```yaml
query_tools:
  enabled: true
  langfuse_timeout_seconds: 15
```

Langfuse base URL/public key/secret key 继续使用 `observability.langfuse` 的现有字段与 env 名。配置加载逻辑在 `observability.langfuse.enabled || query_tools.enabled` 时解析这些值；只有 exporter 保持 S08 的 fail-open 行为。`query_tools.enabled=true` 时，缺 URL、Key、无效 timeout 或无法构造 HTTP client 视为启动配置错误，因为 Tool catalog 不得向 Agent 宣称一个必定不可用的 `langfuse.query`。

包职责如下：

| 包 | 职责 |
|---|---|
| `internal/queryaction/catalog.go` | 无 secret 的 App/ChatGroup 目录 repository 与 response projection。 |
| `internal/queryaction/history.go` | 参数化 MySQL 历史查询、selector 编译、keyset cursor、原文 response。 |
| `internal/queryaction/langfuse.go` | Basic-auth HTTP client、REST request/response、上游分页、无凭据错误映射。 |
| `internal/queryaction/service.go` | Dynamic Tool 名称规范化、严格 arguments decode、ToolResult 包装。 |
| `internal/codexapp` | 完整 catalog provider、schema 与普通/Goal 一致的 archive/start。 |
| `internal/config` | `query_tools`、按需解析 Langfuse credential，禁止序列化 secret。 |
| `internal/storage` | 查询专用只读 repository 与 migration 应用；不保存 query 结果。 |
| `cmd/server` | 构造服务、注入 Store/HTTP client、按 namespace 分发。 |

### 6.6 错误、并发与观察性

所有查询在当前 Tool handler context 下执行，取消即停止 MySQL row scan 或 HTTP request。它们不入 Worker FIFO、不会修改 ChatGroup Thread、不会创建飞书副作用、不会在 MySQL 保存调用或结果；同一 call 的重发允许读到更新后的快照。数据库查询采用现有连接池和 `QueryContext`，rows 必须关闭；Langfuse HTTP response body 必须关闭，response byte 流不得无界读入。

普通 `slog`、workflow、debug timeline 继续不写完整 tool arguments/result；S08 Langfuse 则依既有裁决记录 Dynamic Tool 的完整业务 arguments/result。`langfuse.query` 自己读取到的业务 payload 作为工具结果也可被 S08 原样记录；client 的 Authorization 只存在于 HTTP transport，不得进入任何 observation attribute、ToolResult 或错误。

## 7. 测试设计与验收标准

| ID | 场景 | 验收 |
|---|---|---|
| S09-AT-01 | App/ChatGroup 目录 | Given 两个以上 App、多个 p2p/group ChatGroup，When 任一调用 App 执行 `workspace.list_query_targets`，Then 返回全部 App 与全部 ChatGroup、真实 session ID、当前 Thread/统计，且没有 App Secret/Key。 |
| S09-AT-02 | 无范围跨 App 历史 | Given 多 App 的 interleaved messages，When 任意 App 调用 `history.search({})` 并持续 cursor，Then 所有消息恰好一次、按稳定时间键返回，正文与真实 ID 无截断，查询 SQL 不带调用 route 的 App/ChatGroup/Actor 条件。 |
| S09-AT-03 | selector 与 SQL 安全 | Given App/Group/Trace/sender/status/text/time组合、空 assistant、相同 timestamp 和恶意 LIKE/SQL 文本，When 查询，Then selector 语义正确、cursor 无漏/重、参数化 SQL 不注入，非法 enum/timestamp/cursor 给稳定错误。 |
| S09-AT-04 | Trace bridge | Given 一个 MySQL `trace_id` 和目录 session ID，When 先查 history 后查 `langfuse.query(trace|observations|session)`，Then 所有返回使用原始 ID、可读取完整 I/O/metadata，不伪称消息有历史 Thread ID。 |
| S09-AT-05 | Langfuse 查询模式 | Given fake HTTP server 模拟 traces/detail/observations/session/errors、分页和 401/5xx/超时/坏 JSON/循环 cursor，When 各 mode 调用，Then 路径、query encoding、分页、errors 聚合和稳定错误正确；Authorization 仅由 transport 接收，绝不出现在结果或测试失败输出。 |
| S09-AT-06 | 配置边界 | Given exporter disabled 但 query enabled，When valid Langfuse credential 存在，Then `langfuse.query` 可用；Given query enabled 却缺 URL/Key 或 timeout 非法，Then 启动前失败且 catalog 不启动。 |
| S09-AT-07 | 完整 catalog 升级 | Given 任意旧 Feishu/S06 catalog、`archive_pending/start_pending` 崩溃点和 Schedule 开/关组合，When 普通 Turn 或 `/goal` 到达，Then 只恢复目标完整 catalog Thread，schema/version 一致，并能把三个 query call 路由到 handler。 |
| S09-AT-08 | 只读并发/取消 | Given 同一或不同 Channel 并发查询及 HTTP/DB 阻塞，When cancel/timeout，Then query 停止并关闭 rows/body，没有 message/chat_group/action ledger 副作用，其他 Worker 不受阻塞。 |
| S09-AT-09 | 真实 L3 | Given 新 migration、真实 MySQL、当前自托管 Langfuse Project 与 `codex app-server --stdio`，When 实际注册 S09 catalog 并调用目录、跨 App 历史、Trace/Observation/Session/Errors，Then protocol round-trip 与原始响应均成功。 |
| S09-AT-10 | 真实 L4 跨 App | Given 两个测试 App，When 在 App A 发出查询 App B 历史/Trace 的自然语言请求，Then Agent 先发现目标、再调用 history/langfuse 并给出可见结论；保存 App A/B、ChatGroup、Trace ID、Tool call、进程启动时间和 DB/HTTP 证据。 |

L1 覆盖 selector、cursor、schema、HTTP mapper、catalog version；L2 覆盖 sqlmock/真实 MySQL repository、fake Runtime/HTTP、普通与 Goal catalog state；并发边界运行目标 `go test -race`。L3 使用真实 MySQL、Langfuse REST 和 App Server，L4 使用两测试飞书 App；fake 与静态 JSON 不能替代 L3/L4。

## 8. 最终本地集成校验

### S09-LI-01：从 App A 回溯 App B 的对话与执行 Trace

**目标**：证明调用方 App 没有隐式范围限制，且 MySQL ↔ Langfuse 的关联在真实 Bot 端到端可用。

**前置**：

- `010_s09_query_indexes.sql` 已由新进程应用；`query_tools.enabled=true`，Langfuse URL/Key 有效；
- 以 `./bot_controller.sh build` 构建，再以 `./bot_controller.sh restart` 启动；记录新 PID/启动时间，`/healthz=ok`、`/readyz` receivers connected；
- 两个启用测试 App（A、B）均可收发，App Server 已登录，Langfuse Project 可从本机 API/UI 读回；
- 先在 App B 发送唯一文本 `S09-CROSS-APP-<nonce>`，等待终态并保存其 MySQL `trace_id`、App/ChatGroup ID。

| 步骤 | 操作 | 预期与保存证据 |
|---|---|---|
| 1 | 从 App A 请求“列出可查询应用与会话”。 | Agent 调 `workspace.list_query_targets`；结果含 App B、其 ChatGroup 和 `langfuse_session_id`。保存 Tool call 与原始结果。 |
| 2 | 从 App A 请求查找 App B 中的 nonce 对话。 | Agent 用 `history.search`，返回 App B 用户/助手原文、真实 `trace_id`，没有当前 App A 条件。保存 DB query/readback。 |
| 3 | 要求展开该 Trace 与其操作链。 | Agent 依次用 `langfuse.query(mode=trace)` 和 `observations`；返回与 MySQL 相同 Trace ID 的完整 I/O/metadata。保存 HTTP status、Trace/Observation ID。 |
| 4 | 用目录给出的 Session ID 查询 App B Session，并查询同时间段 errors。 | Agent 调 `session`、`errors`；结果可读且无调用方范围限制。保存 cursor 与 terminal page。 |
| 5 | `/new` 后再次从 App A 发起相同查询。 | 新 Thread 仍注册完整 S09 catalog；旧 Thread 没有被 resume 后补 schema。保存 archive/start、persisted catalog version 和新 Thread ID。 |

失败时按“新进程/配置 → persisted catalog state → App Server call binding → MySQL selector/cursor/index → Langfuse HTTP status/pagination”顺序排查。不得把旧进程、fake HTTP、只有单 App 的查询或仅 CLI 直接请求当作 S09-LI-01 证据。

## 9. Definition of Done

- [ ] Story List 新增 S09，本文与两份设计文档对 catalog、查询边界、MySQL 索引、Langfuse API 和凭据边界同步；无相互矛盾的旧版本说明。
- [ ] 三个 Dynamic Tool schema、完整 catalog provider、普通 Turn 与 `/goal` upgrade 路径均实现；任一运行 catalog/schema 变化必经持久 archive/start。
- [ ] App/ChatGroup 目录和跨 App 历史查询按真实 MySQL 结构工作，所有业务字段原样返回；无调用方范围限制、无业务 Redactor/截断，且无凭据泄漏。
- [ ] Migration 010 及索引、参数化 selector、稳定 keyset cursor、取消/资源释放和无副作用并发有 L1/L2/真实 MySQL 证据。
- [ ] Langfuse Project client 覆盖 traces/detail/observations/session/errors、跨 App/session filter、无范围分页、错误映射和 exporter-disabled 查询；真实 v3 REST endpoint 有 L3 read-back。
- [ ] 通过 `gofmt`、相关 `go test`、目标 `go test -race`、`go vet ./...` 与完整 `go test ./... -count=1`。
- [ ] 使用 `./bot_controller.sh build` 与 `restart` 的新进程已完成 S09-LI-01 L3/L4；保存 A/B、Trace/Session、Thread/Turn、migration/index、HTTP health 和用户可见结论证据。
- [ ] 残留风险、后续 Story 与本地配置/运行说明已同步；不以绿色 fake 或静态 fixture 宣称跨 App 外部验收。

## 10. 风险、残留项与后续 Story

| 风险/残留项 | 缓解与边界 | 后续 |
|---|---|---|
| 原文 Trace/历史很大 | 不以产品范围截断；使用 cursor 分批传输，Agent 持续翻页。HTTP/JSON 物理上限须在 L3 记录，不能悄然摘要。 | 如需可恢复导出，单独设计本地 artifact/outbox。 |
| Langfuse REST schema 漂移 | 以当前 v3 L3 response、fake contract 和真实 read-back 守护；不耦合 ClickHouse 内表。 | Langfuse API compatibility Story。 |
| 老消息没有历史 Thread ID | 以 Trace ID 关联；不猜测当前 Thread 属于旧消息。 | 如确有回溯需求，单独迁移持久 `execution_thread_id/turn_id`。 |
| 单 Project Key 的可见边界 | 本 Story 只读当前 Project；App 跨查询由 metadata/session 达成。 | 多 Project registry/credential discovery 需独立设计。 |
| 查询内容被后续 S08 再观察 | 业务数据允许原文写入 Langfuse；只有运行凭据始终排除。 | 如需查询审计或保留策略，另起不改变读取语义的 Story。 |

### 概设影响

实现获批后，必须同步：

1. `docs/01-codex-appserver-protocol-research.md`：更新 `thread/start.dynamicTools` 的完整 catalog/version 规则、server request 的只读 query ToolResult 示例；
2. `docs/02-redesign-high-level.md`：更新实体查询索引、完整 catalog provider、跨 App 查询产品边界、`query_tools` 配置与 Langfuse REST 查询位置；
3. `README.md`、`config.yaml.template`、`.env.example`：增加 query tools 配置和不记录 Key 的本地运行说明；
4. `docs/story/STORY_LIST.md`：在本 Story 进入 Draft/Ready/Delivered 时同步状态。

### 设计依据

- `/root/investment/.claude/skills/chat_history/SKILL.md` 与 `chat_history_search.py`：历史查询的按 session 分组、关键词/时间/角色/分页交互，但不继承频道隔离或 SQLite。
- `/root/investment/.claude/skills/langfuse_query/SKILL.md` 与 `langfuse_query.py`：traces/observations/session/errors 的调试路径，但迁移到 Bot-owned Project-scoped REST client。
- [S05：附件输入与飞书能力代理](S05-附件输入与飞书能力代理-设计.md)：Dynamic Tool protocol binding 与 result contract。
- [S06：定时任务与 Agent 工具](S06-定时任务与Agent工具-设计.md)：catalog upgrade 持久状态机与真实 L3/L4 交付规则。
- [S08：Langfuse 全链路 Trace 可观测性](S08-Langfuse全链路Trace可观测性-设计.md)：canonical Trace ID、Session ID、明文业务数据与凭据边界。
