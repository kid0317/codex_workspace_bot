# S02：按 Chat 串行的内存 Worker 批队列与流式卡片

> **状态**：Delivered  
> **日期**：2026-07-11  
> **关联概设**：[概要设计 v3](../02-redesign-high-level.md) §2.3、§9.2  
> **依赖**：S01 已交付的 MySQL 入站幂等、ChatGroup、text 飞书 receiver/sender；测试飞书 App

## 1. 一句话目标

让单聊和群聊 text 消息按 App 和聊天身份进入内存 Worker；每个 Worker 将同一轮待处理消息合并成一个 Batch，先建立**一张 Batch 流式卡片**，再返回可人工核验的完整未来 Codex App Server 请求参数，从而验证后续真实编排前的队列、合并和隔离边界。

## 2. 背景

S01 已验证多 App 的 text 飞书收发、`(app_id, chat_type, chat_id)` ChatGroup 持久化隔离、入站幂等和 Message 持久化，但仍同步发送固定回显。未来 Agent 处理时间可能很长：同一聊天上一轮尚未结束时，用户连续发送的四条消息必须保序进入该 Worker 的 FIFO，并在下一次处理时合并为一次 App Server 请求和一次用户可见回复。

S02 只模拟 Worker：它构造参数、等待 5 秒、将参数作为卡片最终内容；不启动 App Server。为避免把“无任务”与“长任务”混淆，Worker 在内存维护显式状态机；只有真正 `Idle` 的 Worker 可进入空闲回收，`InProcess` 最长允许 90 分钟。

## 3. 范围

### 3.1 本期做什么

1. 将 S01 Router 的固定回复替换为 WorkerManager 接入；MySQL 幂等和 Message 创建仍在入队前完成。
2. 分开建立 Worker identity：群聊为 `group:{chat_id}:{app_id}`；单聊为 `p2p:{open_id}:{app_id}`。数据库 ChatGroup 仍沿用 S01 的 `(app_id, chat_type, chat_id)`，不把 Worker key 当作新的持久化身份。
3. 每个 Worker 从 FIFO 原子 drain 一批待处理 Message，为**整批**创建一张 work-mode interactive 流式卡片；不为每条 Message 单独创建卡片。
4. 将整批 FIFO query 合并并构造 `PreparedAppServerRequest`；默认模拟处理 5 秒后，将完整、脱敏的请求参数 JSON 更新到该 Batch 卡片，供用户在飞书中核验。
5. 用 `Idle`、`CreatingCard`、`InProcess`、`Stopping`、`Stopped` 状态机管理 Worker；空闲 30 分钟可回收，InProcess 超过 90 分钟触发优雅停止。
6. 通过 fake processor/output、并发 race 测试与真实飞书 smoke 验证 p2p/group、多 App、批合并、批卡片、状态机与超时。

### 3.2 本期不做什么

- 不启动或调用 `codex app-server`，不发送 `initialize`、`thread/start`、`thread/resume`、`turn/start`，不保存 Codex Thread ID。
- 不做进程退出恢复、持久化队列、自动重试、附件、审批、命令、Langfuse 或真实 delta。
- 不把调试参数卡片当作后续产品 UX：真实 App Server Story 必须重新审查展示内容，不能将 workspace 路径、thread plan 或用户合并输入默认暴露给最终用户。
- 不新增 Message/ChatGroup schema；一个 Batch 的 card message ID 和状态仅在内存。每条 Message 仍使用既有 `feishu_bot_message_id` 记录该 Batch 的同一首个可见消息 ID。
- companion mode 不创建 interactive card：它沿用相同队列和 Batch，但以一条最终 plain text 返回同一份参数 JSON；本期不使用 `[[SEND]]` 分段。

## 4. 依赖与前置条件

| 依赖 | 状态 | S02 使用方式 |
|---|---|---|
| S01 MySQL、Router 幂等与 Message | 已交付 | 在 Worker 接入前创建每条 Message；重复 event 不入队。 |
| S01 飞书事件字段 | 已交付 | group 使用 `chat_id`；p2p 使用 `sender_open_id` 作为 Worker key 和回复目标。 |
| work-mode App 的 interactive message 权限 | 本地人工前置 | 真实 Batch 卡片 L4。 |
| App Server 协议研究 | 已有本地证据 | 仅定义/展示未来请求参数，不运行 App Server。 |

## 5. 核心设计决策

| 问题 | 结论 | 原因 | 后果 |
|---|---|---|---|
| p2p/group 身份 | Worker key 分别使用 open ID 与 chat ID，再加 app ID | 单聊 reply target 是 open ID；群聊 reply target 是 chat ID。 | key 构造必须集中为类型，不允许调用点自行拼字符串。 |
| 卡片归属 | 一次 `Batch` 一张卡片，卡片归 Worker 当前 batch，不归单条 Message | 同一轮多条 query 将组成一次 App Server 请求，也只能有一次对应回复。 | 本 Batch 的每条 Message 指向相同的 card/outbound message ID。 |
| 批边界 | Worker 进入 `InProcess` 前一次性 drain 当前 FIFO；InProcess 期间新消息保留到下一 Batch | 已在上一轮处理期间到达的多条消息自然合并，绝不与当前已开始请求混合。 | 不承诺恰好同时到达的消息一定同批；FIFO 顺序始终不变。 |
| 最终模拟输出 | 以格式化 `PreparedAppServerRequest` JSON 为 Batch 卡片最终正文 | 用户能逐字段核验未来真实请求。 | 仅展示请求参数，不展示 Secret、凭据、原始 approval token 或内部错误。 |
| 长任务/回收 | 仅 `Idle` 且空闲满 30 分钟可回收；`InProcess` 设 90 分钟 deadline | 长达 90 分钟的运行并不空闲。 | 超时转 `Stopping`，取消当前 batch，完成用户可见收尾并停止 Worker。 |
| timeout 后队列 | 当前 batch 以 timeout 终态收尾；尚未开始的 FIFO 任务逐条标记 `worker_timeout_stopped` 并发送重试提示，Worker 从 registry 删除 | 不让未知运行状态继续接收新工作，也不静默丢弃已接管消息。 | 后续新消息会惰性创建新 Worker；本期不重放旧消息。 |
| 多 App 运行时 | 每个 App 在启动时生成不可变 `AppRuntime` 与自身 Feishu output adapter | 防止同群多 App 共享 credential、workspace/model 或卡片 client。 | job 必须拷贝 runtime 值，禁止按名称/全局 sender 反查。 |

## 6. 主链路与数据/接口契约

### 6.1 路由身份与回复目标

| 入站类型 | WorkerKey | 回复 target | ChatGroup 持久化键 |
|---|---|---|---|
| group | `group:{chat_id}:{app_id}` | `chat_id` / `chat_id` | `(app_id, "group", chat_id)` |
| p2p | `p2p:{sender_open_id}:{app_id}` | `sender_open_id` / `open_id` | `(app_id, "p2p", chat_id)` |

`chat_id` 与 `sender_open_id` 都来自同一飞书入站事件；p2p 的 `chat_id` 只用于 S01 ChatGroup 持久化，**不能**替代 Worker key 或 reply target。日志只记录 `app_id`、key 类型、ID 哈希后缀、message/trace/batch ID 和状态，不记录原始 query、open ID、card 正文、workspace 路径或 Secret。

### 6.2 Worker 状态机

```text
                    FIFO append
       ┌──────────────────────────────────┐
       │                                  ▼
     Idle ── drain non-empty FIFO ──> CreatingCard
       ▲                                  │ card created / card-create failed (2s deadline)
       │                                  ▼
       └──── batch finalization ─── InProcess
       │                                  │
 idle >= 30m + no FIFO/no batch            │ process finished
       ▼                                  │
    Stopped <── graceful stop ── Stopping ◄┘
                         ▲
                         └─ InProcess elapsed >= 90m / service shutdown
```

| 状态 | 内存条件 | 允许动作 | 禁止/离开条件 |
|---|---|---|---|
| `Idle` | 无 active batch；可有 FIFO | 接受任务；FIFO 非空时选取整个当前 FIFO 进入 CreatingCard | 仅此状态计入 30 分钟 idle 定时。 |
| `CreatingCard` | 一个已 drain 的 Batch，尚无 card 终态 | 以 2 秒 deadline 创建 Batch card；失败后仍进入 InProcess 并保留 text fallback。 | 不再 drain 新入队消息。 |
| `InProcess` | Batch card 已决议；processor 正在运行或等待 final output | 更新同一 Batch card；接收新消息到 FIFO；90 分钟 timer。 | 新消息不得并入 active batch。 |
| `Stopping` | timeout/shutdown 已触发 cancellation | 只做一次取消、batch timeout 最终化、pending FIFO 重试提示与 registry 条件删除。 | 拒绝新的 Accept 为 `worker_stopping`，不得开启新 Batch。 |
| `Stopped` | goroutine 已退出且不再在 registry 中 | 无。 | 新消息由 Manager 创建新 Worker。 |

90 分钟 timer 从状态成功写入 `InProcess` 开始；processor 必须消费可取消 context。到期后 Worker 先转 `Stopping`、调用 cancel，等待 processor 返回或最多 10 秒的 cleanup deadline；随后将 Batch 卡片更新为“本批处理超时，请重新发送”，将 pending FIFO 逐条以 text 提示“上一批超时，未处理消息请重新发送”，所有 Message 做条件失败转移，最后从 registry 删除并退出。`Idle` timer 从成功完成 Batch、FIFO 为空且状态转为 Idle 时重新计时；绝不从最近消息时间或 goroutine 创建时间推断空闲。

### 6.3 时序、批卡片与并发所有权

```text
Feishu receiver
  -> Router.Validate + PersistIncoming（短 MySQL transaction；duplicate stop）
  -> WorkerManager.Accept(job)
       lock manager + Worker：检查 state=Idle|CreatingCard|InProcess、容量、append FIFO sequence
       unlock
  -> return（不等待 card API 或 5 秒）

Worker goroutine
  Idle + FIFO non-empty
  -> lock Worker：drain FIFO snapshot -> Batch；state=CreatingCard; unlock
  -> CreateBatchCard(batch)  // work interactive card; 2 second deadline, only one call
  -> assign same card message ID to every Batch Message; state=InProcess
  -> PrepareAppServerRequest(batch)
  -> sleep 5 seconds through cancellable Processor.Process
  -> UpdateBatchCard(final PreparedAppServerRequest JSON)
  -> conditionally finalize every Batch Message; state=Idle or immediately begin next Batch
```

Manager 仅拥有 registry 与全局 active Worker 计数；Worker 仅拥有 FIFO、状态、current Batch、last-idle-at 和 cancellation。飞书 API、processor 和 MySQL 不能持 mutex。一个 Worker goroutine 是该 key 唯一 consumer，故只有它可 create/update/finalize 当前 Batch card。不同 Worker 可并行；同 Worker 的 Batch 不交叠。

`Accept` 遇到 `Stopping|Stopped` 不把任务放到旧 FIFO：Manager 在同一 registry 临界区对 `Stopping` 返回 `worker_stopping`，Router 条件失败该 Message 并发确定性 text；`Stopped` 已删除，下一次 Accept 会惰性创建 Worker。队列满和全局 20 Worker 满也走同一“未接管、确定性 text、Message failed”路径。

### 6.4 内存对象与未来 App Server 请求参数

```go
type AppRuntime struct {
    ID, Name, WorkspaceDir, WorkspaceMode, Model, Effort string
    Output Output // per-App Feishu client; read-only after server startup
}

type WorkerKey struct {
    Kind  string // group or p2p
    Peer  string // group: chat_id; p2p: sender_open_id
    AppID string
}

type QueuedMessage struct {
    MessageID, ChatGroupID, TraceID, EventID string
    Runtime AppRuntime
    Key WorkerKey
    ReplyTarget ReplyTarget
    Query string
    Sequence uint64
}

type PreparedAppServerRequest struct {
    BatchID, WorkerKey string
    MessageIDs, TraceIDs []string
    Thread struct {
        Method string // "thread/start" or "thread/resume"; plan only
        Params struct { CWD, Model, Effort, ApprovalPolicy, Sandbox string; ExistingThreadID string }
    }
    Turn struct {
        Method string // "turn/start"; plan only
        Params struct { CWD string; Input []TextInputItem }
    }
}
```

`PreparedAppServerRequest` 是本期唯一的模拟结果。构造规则：

1. `MessageIDs`、`TraceIDs` 与 `Input` 内原始 query 列表严格按 FIFO sequence 排列；不去重。
2. `Thread.Method` 依据 ChatGroup 的未来 thread ID 计划 start/resume；本期无 thread ID 时为 `thread/start`。`CWD/model/effort` 全部来自同一 Batch 的 AppRuntime，`approvalPolicy=never`、`sandbox=danger-full-access` 来自已接受的全局固定契约。
3. `Turn.Method=turn/start`，其 `Params.CWD` 再次强制使用同一 workspace；`Input` 为一个明确的 text item，内容以稳定边界合并本 Batch 的每条 query，例如 `[{"sequence":1,"text":"..."}, ...]` 的 JSON 编码。它不是数组裸字符串，也不是本期真实 JSON-RPC 请求。
4. Batch 完成时，将格式化后的完整 `PreparedAppServerRequest` JSON（包含 input 的原 query）更新到**同一张 Batch 卡片**，标题为“模拟 App Server 请求参数”。不写普通服务日志、不写 Langfuse、不存入新的 DB 字段。
5. 不显示 Feishu App Secret、数据库密码、authorization header、approval token、sandbox credential 或原始内部错误；这些不是 App Server 请求参数，也不属于核验内容。

### 6.5 Output、Message 状态与多 App 接线

`cmd/server` 为每个 enabled `storage.App` 创建 `AppRuntime`，并将该 runtime、而非仅 `ID/Name`，传给 receiver/Router。Router 只依赖 `WorkerDispatcher`；`worker` 消费其 AppRuntime 上绑定的 Output；`feishu` 生产 adapter 使用该 App 自己的 credentials。禁止共享默认 sender。

```go
type Output interface {
    CreateBatchCard(ctx context.Context, batch Batch) (messageID string, error) // work mode only
    UpdateBatchCard(ctx context.Context, cardID string, content string) error
    SendText(ctx context.Context, target ReplyTarget, text string) (messageID string, error)
}
```

对 work Batch，成功 `CreateBatchCard` 返回的同一 `messageID` 条件写入 Batch 内每条 `messages.feishu_bot_message_id`；card create 失败则在 final/timeout 时对每条 Message 的正确 reply target 发送 text，并写各自 text ID。对 companion Batch，不创建 card，完成后向该 Batch 的统一 reply target 发送一次参数 text，并把该同一 text ID 写入每条 Message。

Message 条件状态机为 `received -> processing -> succeeded|failed`，拒绝为 `received -> failed`。repository 方法使用 `WHERE id=? AND status IN (...)` 并返回是否实际改变；慢到的 card/update/timeout 不得覆盖已终态。一个 Batch 只能包含相同 WorkerKey，所以其 reply target 一致；群内多个用户的 query 可能被共同显示在该群 Batch card，这是**仅 S02 本地人工核验**的明确行为。

## 7. 测试设计与验收标准

| 编号 | Given / When / Then |
|---|---|
| S02-AT-01 | Given 新 group Message，When Router 持久化并 Accept，Then `group:{chat_id}:{app_id}` FIFO 追加且 handler 不等待 card API/5 秒。 |
| S02-AT-02 | Given 新 p2p Message，When Accept，Then key 仅含 sender open ID，reply target 为 open_id；p2p ChatID 不参与 Worker key。 |
| S02-AT-03 | Given active Batch 正在 InProcess，When 同 key 再到四条 Message，Then 四条依 FIFO 留在 pending queue，当前 Batch 不变；当前完成后四条被一次性 drain、只 create 一张 Batch card、只调用一次 processor。 |
| S02-AT-04 | Given 可控 drain gate 和三条同 key Message，When gate 释放，Then Batch 的 MessageIDs、TraceIDs、input query list 和 final card 参数 JSON 保持相同 FIFO 顺序。 |
| S02-AT-05 | Given group 与 p2p 或两个不同 app key，When 同时处理，Then processor 可并行，不能共享 Batch、卡片、runtime 或 query。 |
| S02-AT-06 | Given 同一 ChatID 的两个 App，When 触发两者，Then 各自 WorkerKey/AppRuntime/output client 独立，参数中的 CWD/model/effort 不交叉。 |
| S02-AT-07 | Given 同一 event 重放，When Router 再收到，Then 不再 Accept、不创建第二张 Batch card。 |
| S02-AT-08 | Given 20 个 non-Stopped Worker 或某 FIFO 达 64，When 新消息到达，Then 未接管、无 Batch card、Message 条件 failed 并发送确定性 text。 |
| S02-AT-09 | Given Worker InProcess 已 89 分钟且 FIFO 为空，When idle scanner 执行，Then Worker 不回收。Given Idle 已 30 分钟且 FIFO/current batch 均为空，Then 条件删除并停止。 |
| S02-AT-10 | Given InProcess 到达 90 分钟，When processor 响应 cancel，Then Worker 仅一次进入 Stopping，Batch 卡更新 timeout、pending FIFO 获重试提示、所有相关 Message 合法 failed，Worker 被删除。 |
| S02-AT-11 | Given processor 未在 cancel 后 10 秒返回，When cleanup deadline 到达，Then 不启动后续 Batch，记录脱敏超时并结束 Worker goroutine；后续消息新建 Worker。 |
| S02-AT-12 | Given work-mode Batch，When CreateBatchCard 成功，Then Batch 内每条 Message 保存相同首个可见 message ID；final Update 不改变 ID。 |
| S02-AT-13 | Given card create/update 失败，When Batch 完成，Then 使用 text fallback；所有失败路径无 secret/raw internal error，且不丢失已接管 Message。 |
| S02-AT-14 | Given `PreparedAppServerRequest`，When fake processor 完成，Then final Batch card/text 内容等于期望格式化参数 JSON，包含所有 query/input、CWD/model/effort/method，而无 Secret。 |
| S02-AT-15 | Given concurrent Accept、drain、card API 延迟、idle scan、90 分钟 timeout 与 shutdown，When `go test -race`，Then 无 race、死锁、重复 batch 或跨 App/p2p/group 污染。 |

测试用可控 clock、processor/card gates 模拟 5 秒、30 分钟和 90 分钟，不能真实等待。运行 `gofmt`、`go vet ./...`、`go test ./... -race`；fake 只能证明本地契约。

## 8. 最终本地集成校验

### S02-LI-01：真实 p2p/group Batch 卡片与参数核验

**前置**：S01 MySQL、服务、work-mode 测试 App、interactive 权限均可用；另有一个仅包含该 App 的测试群。双 App 群测试预先记录飞书会向哪些 App 投递同一普通消息。

| 步骤 | 操作 | 预期与证据 |
|---|---|---|
| 1 | 用个人飞书向 Bot 发送唯一 p2p text。 | 得到一张 Batch 卡；约 5 秒后同卡显示 `p2p:{open_id}:{app_id}` 的参数 JSON。保存卡片、trace/message ID、日志。 |
| 2 | 在仅含 App A 的测试群发一条消息，等待其进入 InProcess 后连续发四条唯一消息。 | 第一批与第二批各一张卡；第二卡参数 JSON 的 input 精确含四条 FIFO query。保存两张卡截图和 batch logs。 |
| 3 | 在双 App 群发送唯一消息。 | 记录实际投递数；每个被投递 App 有独立卡/参数、不同 App ID/key/runtime。若未双投递，明确记录为飞书前置不满足。 |
| 4 | 查询 MySQL 与日志。 | 每条 Message 有正确最终状态和 Batch 共用的首个可见 message ID；日志能按 trace 找到 worker state/batch ID，但不含 query/open ID/workspace/Secret。 |

**失败处理**：先检查 `feishu_connected`、卡片权限、work mode 和群成员；再按 trace/batch ID 检查 Worker 状态与 Message 条件转移。参数卡存在但字段不对视为本 Story 验收失败，不能仅以“收到回复”通过。

**2026-07-11 当前证据**：真实 p2p App `3ae8c44d-7cbd-11f1-b6a8-6a15809e6766` 已完成两轮 work-mode 验收。第二轮 Batch `batch-2` 包含 5 条 FIFO Message，trace IDs 为 `7a6fc7e65e26454bb79fa7cf08c9993d`、`5ee6cd6dbbb5e3e13ed769faa1ec110c`、`4c0516eec64da74d93e1315ea8134830`、`4fd43109f9cdffec5b2d7c344193badb`、`c69012a7c577c6a6018c6b150550a3e6`；五条记录均为 `succeeded` 且共用 card message ID `om_x100b6a28685a7ca4b3c3d7e441c6afc`。落库 `assistant_content` 与用户卡片均包含相同的 `PreparedAppServerRequest`，其中 FIFO input 是 `["1","23","4","5","62"]`。当时运行进程 healthz 为 `ok`，两个 receiver 均为 `connected`。

## 9. Definition of Done

- [x] S02-AT-01 至 S02-AT-15 实现完成，含 Worker 状态机、90 分钟超时和目标 race 覆盖。
- [x] `gofmt`、`go vet ./...`、`go test ./... -race` 通过。
- [x] README、配置模板、Router/Message 状态说明和 HLD 影响检查同步；未新增持久化队列或恢复语义。
- [x] S02-LI-01 已在新进程和真实飞书上执行；Batch 卡片、参数核验、trace/message ID、日志和 MySQL 证据见本节。
- [x] 已明确本期参数回显是调试验收契约，不能未经重新评审带入真实生产输出。

## 10. 风险、残留项与后续 Story

| 残留 | 本期行为 | 后续 |
|---|---|---|
| 进程崩溃 | 内存 FIFO/current batch 丢失；用户重新发送。 | 恢复/补偿 Story。 |
| timeout 后 pending 消息 | 明确失败并提示重发，不重放。 | 可恢复队列 Story。 |
| 调试参数回显 | 仅用于本机测试群验证，可能在群内包含本批 query。 | 真实 App Server Story 重新设计用户输出。 |
| 卡片 API 可靠性 | 一次 text fallback，无重试。 | 输出可靠性 Story。 |
| Thread/Turn 调用 | 只展示计划参数。 | App Server 接入 Story。 |

### 评审与决议记录

- 2026-07-11：独立技术评审的初稿整改记录见[评审与整改报告](../archive/story-reviews/S02-技术架构评审与整改报告-2026-07-11.md)。
- 2026-07-11：用户进一步裁决：Batch（一批 query）而非单 Message 对应一张卡；最终回显完整未来 App Server 参数；p2p Worker key 使用 open ID、group 使用 chat ID；以 Worker 状态机定义 30 分钟 idle 与 90 分钟 InProcess timeout。本文已按该裁决重写，原评审报告保留为初稿历史。
- 2026-07-11：实现独立评审发现并整改 timeout 终态、Worker goroutine 回收、文本 fallback、companion 分支和状态机一致性；最终 blocker-only 复审通过。非阻塞残留：companion timeout 的二次条件失败写入可在后续清理，不覆盖既有 failed 状态。
