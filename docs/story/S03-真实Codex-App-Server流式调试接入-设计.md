# S03：真实 Codex App Server 流式调试接入

> **状态**：Ready  
> **版本**：v1.6（评审与用户裁决后）  
> **日期**：2026-07-11  
> **关联概设**：[概要设计 v3.5](../02-redesign-high-level.md) §4.1、§4.3、§5、§6、§9.1、§11.3  
> **依赖**：S01、S02 已 Delivered；可登录的本机 Codex CLI；Docker MySQL；测试飞书 App（work-mode）

## 1. 一句话目标

让测试飞书用户的一批 text 消息真正驱动 Codex App Server 的一个 `turn/start`，以完整原始 debug 日志观察该 turn 的每一条 JSON-RPC 事件，并在终态把原 Batch 卡片更新为“本轮已完成”。

## 2. 背景

S02 已交付 App 隔离的 p2p/group Worker FIFO、Batch 卡片、MySQL Message 状态机和真实飞书 card PATCH。但它的 `PreparedAppServerRequest` 仅是本地验收用的**未来参数回显**：Worker 休眠后把其中的 JSON 显示到卡片，没有启动或调用 App Server。

本 Story 首先跑通真实 Codex 路径，而不是提前实现最终卡片体验。服务启动时创建并握手本 bot 独占的 stdio child；收到普通消息时 Worker 必须确保 ChatGroup 的 Codex Thread，并真实发送 `turn/start`。读取循环要接收所有 App Server 消息，并在 debug 层逐行留下原始 JSON-RPC 证据，供下一 Story决定哪些事件、怎样节流地回吐飞书。

本机在 2026-07-11 已以 `codex-cli 0.144.1` 生成实验 schema/TypeScript bindings 并完成 `initialize` 握手。生成的 `TurnStartParams` 明确要求 `threadId` 与 `input`；`cwd`、`model`、`effort`、`approvalPolicy` 是可选但会影响后续 turns 的覆盖字段。因此 S02 的展示对象不能直接作为 wire request。

## 3. 范围

### 3.1 本期做什么

1. 新增唯一的、并发安全的 `codexapp` App Server 客户端：一个连接只有一个 stdout 读取循环、一个串行 stdin writer、以 JSON-RPC id 关联 client request/response，并按 `threadId`/`turnId` 将通知路由到所属 active turn。
2. 服务启动时创建本 bot 独占的 `codex app-server --stdio` child，并在唯一 client 的连接上完成 `initialize`；只有握手成功才进入 ready。child 异常退出时仅失败本 bot 的 active turn，single supervisor 为该退出 generation 创建一个 replacement；replacement initialize 失败即进入 `app_server_unavailable`，不得自动重放已中断 Batch。任何成功 initialize 的 replacement 都重置连续失败链，之后新的运行中退出可再次尝试 replacement。
3. 将 S02 的 `Processor` 替换为真实 `EnsureThreadAndStartTurn`：读取/写入 `chat_groups.codex_thread_id`，先 `thread/resume`，失败才 `thread/start`，随后发送真实 `turn/start` 并等待该 turn 的终态通知。
4. 以当前生成 schema 的字段构造文本输入：`input: [{"type":"text","text": ...}]`。Batch 内的消息仍按 FIFO 排列并作为一个明确的文本包传入；每个 `thread/start`、`thread/resume` 和 `turn/start` 都显式带同一已验证的绝对 `cwd`。
5. 监听并处理所有 server→client JSON-RPC 消息类别：response、notification、server request 和 error。debug 模式将每一条原始 server→client JSON-RPC 行完整落盘，包括未知的新 method；`turn/started` 记录 active turn，`turn/completed` 作为成功/失败/中断的权威终态。
6. 维持 S02 的首张 work-mode interactive card，但不展示任何 delta、工具输出、推理或请求参数。终态成功 PATCH 为固定“本轮已完成”；失败/中断/超时 PATCH 为确定性安全文案。companion mode 同样不再回显参数，只发送对应固定终态文本。
7. 增加 fake App Server 的协议、生命周期、事件路由和并发测试；增加显式的本机真实 App Server L3 smoke 以及真实飞书 L4 验收步骤。
8. 明确当前个人本地 ingress mode 为 `all`：enabled App 的有效 p2p/group text 均可处理；不为 S03 新增 `AllowedChats` 表、自动白名单或 sender filter。

### 3.2 本期不做什么

- 不把 `item/agentMessage/delta`、reasoning、command output、file patch、plan 或任何原始 App Server payload 显示给飞书；流式卡片渲染、节流和最终自然语言内容属于后续 Story。
- 不实现 `/new`、`/stop`/`/cancel`、`/status`、`turn/steer`、retry、附件、富文本、Langfuse、持久化 approval 或飞书按钮；完整 approval/permission/elicitation 的业务语义也不在本期。S03 仅对任意 server request 回通用 JSON-RPC error 后安全收尾。
- 不接管、杀死或复用任何外部 `codex app-server` 进程；stdio 的 stdin/stdout 属于其创建者，无法安全附着。不得使用 shared daemon/proxy，也不得用 `pkill`、按进程名 kill 或重启其他工具的 App Server。
- 不把完整原始事件回吐飞书、写入 MySQL 或接入 Langfuse；它们仅在本 Story 的个人本地 `logging.level=debug` 验收中写入本地日志。关闭 debug 后恢复为普通 `info` 日志。
- 不改变 HLD 的独占 stdio child、多 workspace、MySQL 和 channel 串行契约；shared daemon/proxy 是明确排除的后续可选研究，不属于 S03。

## 4. 依赖与前置条件

| 依赖 | 状态 | S03 使用方式 |
|---|---|---|
| S01 MySQL `apps/chat_groups/messages` 与入站幂等 | 已交付 | 读取和条件写入 `chat_groups.codex_thread_id`；保持 Batch Message 状态机。 |
| S02 Worker、Batch、interactive card PATCH | 已交付 | 保持同 key 串行和同一 card message ID，只替换 processor 和终态内容。 |
| Codex CLI 0.144.1 或兼容版本 | 本机已验证 | `app-server --stdio`、schema 生成和 `initialize`。上线前重新记录实际版本。 |
| Codex 登录态 | 人工前置 | `codex login` 可用；不把 `~/.codex/auth.json` 复制进项目或日志。 |
| 绝对且可访问的 App `workspace_dir` | S01 启动已校验 | 每次 thread/turn 显式传入，并由 App Server 加载对应指令。 |
| 测试飞书 App 和 work-mode interactive 权限 | S02 已验证 | L4 发送唯一输入、观察同一张 card 终态。 |
| 当前 ingress policy | 已裁决 | `all`：enabled App 的有效 p2p/group text 全部接受；不配置/维护 allow list。 |

## 5. 核心设计决策

| 问题 | 结论 | 原因 | 后果 |
|---|---|---|---|
| App Server 复用 | **已裁决：不复用。**bot 启动只创建自己独占的 `codex app-server --stdio` child；任何外部 App Server 都不接管、不终止。 | stdio transport 只能由创建者持有；独占 child 让配置、事件、EOF 和 restart 归属一个 bot。 | 放弃跨 bot 进程复用；S03 不需要 daemon/proxy、socket 权限或其它 client 的保护规则。 |
| 启动与运行中恢复 | child 启动后在 30 秒内完成 `initialize`；启动/握手失败则本次 bot boot 失败。运行中 child EOF/exit 时，单一 supervisor 使该 generation 的 active turn 失败一次并立即创建一个 replacement child；replacement 启动/握手失败才进入 `app_server_unavailable`，停止本次连续恢复。 | process alive 不证明协议可用；启动/initialize 连续失败大多是登录、配置或环境根因，循环重启不能修复。 | 每个成功 initialize 的 replacement 重置连续失败链；它以后若再次退出，按新的退出事件重新尝试 replacement。只有 replacement 初始化失败时 receiver 保持连接但新的入站 Message 标记 `app_server_unavailable` 并返回确定性失败文本，待人工修复后重启 bot。 |
| 进程归属 | bot 拥有 child 的 stdin、stdout、lifetime 与退出收尾；bot 正常关闭时关闭 child。 | 满足“随服务启动/结束”的产品方向，并把 blast radius 限定到本 bot。 | bot 重启会中断自己的 active turn；下一条消息按 `thread/resume`/fallback 继续。 |
| Ingress 授权 | **已裁决：`all`。**enabled App 的有效 p2p/group text 不受 Chat allow list 限制；topic_group 仍按既有行为忽略。 | 这是个人自用的本地工具；当前代码与 S01 用户决议也没有 Allow list 数据模型。 | B2 关闭；未来若需要限制，按 `(app_id, chat_type, chat_id)` 新开 Story，不把空配置解释为拒绝。 |
| Thread 持久化 | `chat_groups.codex_thread_id` 是唯一 Thread 来源；无值用 `thread/start`，有值先 `thread/resume`，resume 失败再 start 并原子替换。 | 字段和 HLD 已存在，不能新增平行 Session 真相源。 | 增加 repository 的 Get/Set/conditional replace 方法；`PreparedAppServerRequest.Thread` 删除为真实动作。 |
| 真正的请求 shape | `thread/start` 传 `cwd/model/approvalPolicy:"never"/sandbox:"danger-full-access"`；`turn/start` 传 `threadId/cwd/input/model/effort/approvalPolicy:"never"` 与不可复用的 `clientUserMessageId=attempt_id`。 | 当前 0.144.1 schema 和实际 handshake 已校验；`turn/start` 的 input 项是 `{type:"text",text:string}`，response 必返 `turn`。 | 使用显式 Go protocol structs；不能把 S02 的 `effort`、`sandbox` 或裸 query array 当作 turn payload。 |
| 同 channel 串行 | 一个 channel Worker 等待自己的 `turn/completed` 后才释放下一 Batch；不同 Worker 可并发，但共享 client 的写入和 pending map 受锁保护。 | App Server 同 thread 只能有一个 active turn，S02 已保证 channel FIFO。 | notification 必须按 thread+turn 路由，不能被别的 worker 消费。 |
| debug 观察与时间线 | `logging.level=debug` 时，唯一 reader 在 classification 前同步 `Write+Sync` 每条原始 server→client JSON-RPC 行到 raw 文件，并为同一序号写一条结构化 event index；dispatch 后再追加同序号的 outcome。未知 notification 不丢弃。 | S03 的直接目标是掌握真实 event 的完整 shape、维度、时间顺序和本地处理结果，而不是只看字段摘要。 | raw、event 与 outcome 都和普通 `server.log` 分离；不回吐飞书、不写 MySQL/Langfuse。任一 writer 失败使本 generation 进入 `debug_capture_failed`，关闭 child 并使 active collector 失败；该次证据明确标记 incomplete，不能静默漏 event 或伪造完整时间线。 |
| server request | S03 不枚举 approval/permission/elicitation 的业务 response：任意 server request 一律完整写入 debug JSONL、以相同 request ID 回 JSON-RPC error `{code:-32001,message:"unsupported by S03"}`。只有带已绑定 `thread_id`/`turn_id` 的 request 才使所属 attempt 失败；无可路由身份的 unknown request 只记录和拒绝。 | Client 仍满足“server request 必须回 protocol response”，但不提前实现人类审批或 provider 特定语义，也不让 global loose handler 猜测所属 Worker。 | 不仅因 `-32001` 关闭仍健康的 child；若 server 随后 EOF/exit，再走 child-exit 恢复。后续 Approval Story 以持久化审批 broker 替换该通用拒绝器。 |
| 用户可见终态 | work card 成功只显示“本轮已完成”；失败、超时、中断显示确定性安全文本；card PATCH 失败使用现有 text fallback。 | 本期验证 transport 与事件，不把调试数据泄给群聊。 | `assistant_content` 保存同一固定终态文本，不保存 raw event/delta。 |

### 5.1 已裁决的启动与恢复状态机

```text
server main
  -> validate enabled App workspace_dir
  -> start owned `codex app-server --stdio` child
  -> create single client -> initialize
       success -> start HTTP/Feishu receivers
       start/initialize failure -> startup failure; do not start receivers
  -> child EOF/exit while running -> supervisor fails that generation's active batches once
       -> start replacement child -> initialize success: reset recovery chain; continue
       -> initialize failure: app_server_unavailable; no in-process retry; wait for human bot restart

任何外部 App Server：不探测、不接管、不终止、不重启。
```

`initialize` 必须在 child stdio 连接上的任何其他方法之前发送。最小 payload 为 `clientInfo.name/version/title` 和 `capabilities.experimentalApi=true`；实际握手响应至少记录 CLI user-agent、`codexHome`、`platformFamily`、`platformOs`。supervisor 是 child generation、reader、writer 与 replacement 的唯一所有者；旧 generation 的 EOF、close 或 timer 不得关闭/替换已经成功初始化的新 generation。

### 5.2 Turn 超时、interrupt 与唯一终态（B3）

每个 Batch 的 Worker 在写出 `turn/start` 前创建一个不可复用的 `attempt_id` collector，并把同一值放进 `clientUserMessageId`。collector 先只以 `thread_id` 暂存早到 notification；**只能以该 `turn/start` RPC response 的 `result.turn.id` 正式绑定 `turn_id`**。response 前收到的 event 仅在其 `thread_id` 和 `turn_id` 都等于该 response 返回值时才释放给 collector；其它 duplicate/mismatch/late event 只落 debug JSONL。collector 是唯一能通知 Worker 结束本 Batch 的对象，所有成功/失败候选通过 `finishOnce` 竞争，首个获胜者才可 PATCH/写 MySQL/释放 Worker。

| 时点/事件 | collector 动作 | 最终语义 |
|---|---|---|
| 写出 `turn/start` | 启动 3000 秒总时限和 30 秒 RPC-response 时限；response 前只暂存带 thread/turn 的 notification。 | `turn/start` response 的 `turn.id` 是唯一绑定依据，response 不是成功终态。 |
| 收到匹配 `turn/start` response | 绑定其 `result.turn.id`，释放同 thread+turn 的暂存 event；此后任一匹配 notification 刷新 3000 秒无进展时限。 | response 迟到、缺失或与暂存 event 不匹配均不能猜测归属。 |
| `turn/completed` | 以 `turn.status` 判定：`completed` 成功；其它 status 失败。 | 立即 `finishOnce`；迟到/重复 completed 只记 debug。 |
| 总时限或无进展到期，且已绑定 `turn_id` | 原子地进入 `Stopping`，只发送一次 `turn/interrupt(threadId, turnId)`，开始 10 秒 grace。 | grace 内收到 completed 仍以其 status 收尾。 |
| `turn/start` response 30 秒到期而未绑定，或 grace 到期仍无 completed | 该 owned child 已不能可靠地确认/中断本 attempt；关闭它，交给 supervisor 对该 generation 的 replacement 流程。 | 所有受该 child 影响的 active collector 以 `turn_start_unbound`、`turn_timeout` 或 `app_server_exited` 失败一次；不重放。 |
| child EOF/exit、writer error 或 raw debug writer error | runtime 关闭该 generation；每个 active collector `finishOnce(failed)`。 | Worker 释放；supervisor 为该退出 generation 创建一个 replacement child；其 initialize 成功即重置连续失败链。 |
| generic server request error | 回同 ID `-32001`；仅在 request 带已绑定 thread/turn 时对所属 attempt `finishOnce(failed)`。 | 无可路由身份时只记录和拒绝；不关闭仍健康的 child，后续 Batch 仍可继续。 |

S03 不复用现有 90 分钟 simulated processor timeout 作为真实 Turn 语义；实现以可控 clock/timer 测试 30 秒、3000 秒、3000 秒和 10 秒边界。`/cancel` 的用户 UX 留给后续 Story，但这个内部 timeout interrupt 是本期的运行时收尾行为。

### 5.3 Thread CAS 与 resume fallback（B4）

Worker Message 必须携带 `chat_group_id`；storage 为该 ID 提供 `GetChatGroupThread` 和 `SetThreadIfExpected(groupID, expectedThreadID, replacementThreadID) (applied bool, err error)`。后者以 `WHERE id=? AND codex_thread_id <=> ?` 条件更新，`NULL` 用空 expected 表达；任何 DB error 或 `applied=false` 都不能继续使用本次新建的 Thread 发 `turn/start`。

| 情况 | 动作 |
|---|---|
| 当前 Thread 为空 | `thread/start` 返回 `T1` 后，仅当 `SetThreadIfExpected(group,"",T1)` 成功才对 T1 start turn。CAS 失败则 best-effort archive T1、重新读一次；读到已有 Thread 才 resume，读到空值则安全失败，不再二次 start。 |
| 当前 Thread 为 `T0` 且 resume 成功 | 用 T0 start turn；不写 DB。 |
| 当前 Thread 为 `T0` 且 resume 失败 | `thread/start` 得到 T1；仅当 `SetThreadIfExpected(group,T0,T1)` 成功才使用 T1。CAS 失败后 archive T1、重读；若读到另一个 Thread 则只尝试 resume，不能覆盖它。记录 `resume_fallback_started_new_thread`。 |
| `/new` 或其它合法写与本 Batch 竞争 | CAS=0 是预期保护，不得覆盖较新的 NULL/T2，也不得让旧 Batch 在未持久化 Thread 上继续。 |

### 5.4 已裁决事项与仍需约束的事项

1. **已裁决：bot 独占 stdio child。**不复用 daemon/proxy 或外部 App Server；这已经同步到 HLD §3.2。
2. **已裁决：个人 ingress mode 为 `all`。**不实现 Allow list；B2 关闭。
3. **已裁决：不增加 App Server readiness 组合门禁。**child 的 initial initialize 失败使 bot startup 失败；运行中 health/ready HTTP 不作为 App Server liveness 证明或 S03 验收门槛。B6 按此范围关闭。
4. **已裁决：S03 debug 保留完整原始 event。**个人本地测试时，`logging.level=debug` 自动将 server→client 的原始 JSON-RPC 行同步写入独立 JSONL；不做脱敏、hash、留存或访问控制设计。它不改变飞书、MySQL 或 Langfuse 的输出边界；测试完成后切回 `info`。

## 6. 主链路与数据/接口契约

### 6.1 正常时序

```text
Feishu text -> Router PersistIncoming (idempotent) -> channel Worker FIFO
  -> create existing “正在处理” card; received -> processing
  -> ChatGroup ThreadID?
       empty: thread/start(cwd, model, approvalPolicy, sandbox) -> CAS persist returned thread.id
       present: thread/resume(threadId, cwd, ...) -> on error thread/start -> CAS replace ThreadID
  -> turn/start({threadId, cwd, model, effort, approvalPolicy:"never", input:[text]})
  -> reader raw Write+Syncs every server→client line before classification
  -> turn/start response.result.turn.id binds active turn ID; early matching notifications are released
  -> turn/completed(status=completed) -> PATCH same card “本轮已完成”
  -> processing -> succeeded; release Worker
```

`turn/start` 的 request response 只代表请求已接受，不代表 turn 完成；notification 甚至可能在 response 之前到达。Processor 必须先登记 pending turn collector，再写 `turn/start`；仅 response 的 `result.turn.id` 可绑定该 collector，且只以匹配的 `turn/completed` 为成功终态。

### 6.2 wire structs（以本机 0.144.1 generated schema 为准）

```json
{"jsonrpc":"2.0","id":"rpc-101","method":"thread/start","params":{
  "cwd":"/absolute/workspace","model":"<apps.model>",
  "approvalPolicy":"never","sandbox":"danger-full-access"
}}
```

```json
{"jsonrpc":"2.0","id":"rpc-102","method":"turn/start","params":{
  "threadId":"<returned-or-resumed-thread-id>","cwd":"/absolute/workspace",
  "model":"<apps.model>","effort":"<apps.reasoning_effort>",
  "approvalPolicy":"never","clientUserMessageId":"<attempt-id>",
  "input":[{"type":"text","text":"以下是按到达顺序合并的消息：\\n[...FIFO messages...]"}]
}}
```

`thread/resume` 至少传 `threadId` 和 `cwd`，并重新应用本期固定 `approvalPolicy`/`sandbox`、App model。对 RPC response，`id` 允许 string 或 integer；实现内部使用不可重用的 string ID。所有 stdout 行均解码为互斥的 response、notification、server request 或 JSON-RPC error；非 JSON/超限行是协议错误，触发连接失效路径。

### 6.3 事件观察与终态规则

| 入站类型 | S03 动作 | 对 Worker 的影响 |
|---|---|---|
| `turn/started` | 原始 event 已 Write+Sync 到 debug JSONL；response 已绑定同一 `turn.id` 时才交给 collector。 | 更新 in-memory active turn；response 前只暂存。 |
| `item/started`、`item/completed`、任意 `item/*` delta | 原始 event 无条件 Write+Sync 到 debug JSONL；只在已绑定的 thread+turn 匹配时交给 collector；不写飞书、不存 MySQL 正文。 | 不改变终态。 |
| `thread/*`、`account/*`、`warning`、`model/*`、`process/*`、未知 notification | 完整原始 event Write+Sync 到 debug JSONL；未知方法不会被丢弃。 | 不改变终态。 |
| `error` notification | 完整原始 event Write+Sync 到 debug JSONL；若与已绑定 turn 匹配且随后到 `turn/completed`，以后者状态为准。 | 仅在连接/turn 已明确不可继续时提前失败。 |
| `turn/completed` | 完整原始 event Write+Sync 到 debug JSONL，校验已绑定 thread/turn 及 status。 | `completed` 成功；其它 status 按失败/中断处理。 |
| server request | 完整原始 request Write+Sync 到 debug JSONL，以相同 request ID 回通用 JSON-RPC error `-32001 unsupported by S03`；仅当 request 带已绑定 thread/turn 时使所属 attempt 失败。 | 无可路由身份的 unknown request 只记录和拒绝；不实现 approval/permission 业务语义，也不关闭健康 child。 |
| App Server EOF/exit、writer error、debug writer error 或未绑定 response timeout | 关闭该 generation 的 pending request；本轮失败，不自动重放用户消息。 | supervisor 为该退出 generation 创建一个本 bot owned replacement child；replacement initialize 成功则恢复并重置连续失败链，失败后进入 `app_server_unavailable`。 |

连接退出时，所有 in-process Batch 变为 `failed`（安全错误码 `app_server_exited`），Worker 释放；**不**自动重放已经请求的 Batch。replacement child 成功初始化后，下一条普通消息走 `thread/resume`，失败时以 `resume_fallback_started_new_thread` 事件记录并新建 Thread。replacement initialize 失败时不再循环 restart；后续入站持久化后以 `app_server_unavailable` 失败，待人工重启 bot。这与 HLD 恢复契约一致。

### 6.4 数据、状态与日志契约

- 不新增表：`chat_groups.codex_thread_id` 已在 `001_initial.sql` 中存在。入队后 Worker Message 携带 `chat_group_id`；group Worker key 固定为 `group:{chat_id}:{app_id}`，p2p Worker key 固定为 `p2p:{sender_open_id}:{app_id}`，但两者都以事件的 `chat_id` 定位 ChatGroup。repository 以 `chat_group_id` 读写 Thread ID，并以 `WHERE id=? AND codex_thread_id <=> ?` 条件更新，避免旧 Batch 覆盖 `/new` 或后续有效 Thread。
- `codexapp` 对 Router 暴露只读 `Availability() Ready|Unavailable`。receiver 的 receipt/idempotency 与 `PersistIncoming` 先执行；若其后为 `Unavailable`，Router 不入 Worker queue，而是条件 `FailMessage(messageID,"app_server_unavailable",...)` 并发送固定失败文本。此检查和 processor 侧的同名失败都必须幂等，避免 availability 变化时丢失或重复同一入站事件。
- Batch 全部 Message 仍为 `received -> processing -> succeeded|failed`。终态 `assistant_content` 是固定用户可见结果，`duration_ms` 从真实 turn 开始到终态计算；不写 protocol JSON 或 delta。
- `active turn` 仅由拥有该 channel Worker 的 goroutine 变更；client 只派发事件，不能直接更改 Worker queue/card/DB。
- 普通结构化日志带 `app_id`、`channel_key`、`session_id`（本项目无 session 时省略）、`thread_id`、`turn_id`、`event`、`error`。debug raw 与 timeline 文件的精确契约见下一节；任一 writer 失败记录 `debug_event_write_failed` 后立即关闭当前 generation；该次真实验收不能宣称 event 证据完整。

### 6.5 Debug Event Timeline 契约

每次 bot process 在 `logging.dir` 中创建同一 `<process-start>` 的三个文件：

```text
appserver-raw-<process-start>.ndjson        # 第 N 行 = seq N 的完整原始 server→client JSON-RPC 行
appserver-event-<process-start>.jsonl       # 第 N 行 = seq N 的 classification 前后可确定的结构化事件索引
appserver-outcome-<process-start>.jsonl     # 引用 seq N 的 dispatch 后处理结果；与 event 文件 join
```

唯一 reader 对每个收到的 stdout 行按固定顺序执行：`seq++` → raw 文件 `Write+Sync` → JSON-RPC classification/提取 event 维度 → event 文件 `Write+Sync` → dispatch → outcome 文件 `Write+Sync`。raw 与 event 的第 N 行、`seq=N` 一一对应；outcome 以 `seq` 关联，按该值 join。`observed_at` 是 RFC3339Nano UTC 时间，`elapsed_ns` 是从 bot process 启动单调计时得到的相对时间，后者用于避免系统时钟调整打乱时间线。raw 或 event 写入失败时不得 dispatch；outcome 写入失败发生在 dispatch 后，必须立即关闭 generation、标记 `debug_capture_incomplete` 并使该次 L3/L4 证据失败，绝不把不完整 outcome 当完整时间线。

每条 event JSONL 都必须有下列字段；不能从缺失字段猜测身份，未知值写 `null`：

| 维度 | 字段 | 含义 |
|---|---|---|
| 顺序与进程 | `seq`, `observed_at`, `elapsed_ns`, `generation` | 事件全局顺序、绝对/单调时间、所属 child generation。 |
| JSON-RPC 分类 | `direction`, `message_class`, `method`, `rpc_id` | 固定 `server_to_client`；`response`、`notification`、`server_request`、`jsonrpc_error` 或 `protocol_error`；method 与 request/response ID。 |
| 产品路由 | `app_id`, `channel_key`, `chat_group_id`, `attempt_id` | 能映射时填写 owning App、Worker、持久化 ChatGroup 和本地 attempt。 |
| App Server 路由 | `thread_id`, `turn_id`, `item_id` | 从 raw event 提取的 Thread、Turn、Item 身份。 |
| 分类时路由快照 | `route_snapshot` | `unbound_candidate`、`bound_candidate`、`unroutable`、`connection_failed` 等；只记录 classification 时已知的路由事实。 |

每条 outcome JSONL 至少有 `seq`、`outcome_at`、`elapsed_ns`、`route_state`、`dispatch_result`、`error`。`route_state` 记录 `unbound_buffered`、`bound_dispatched`、`ignored_late`、`unroutable`、`connection_failed` 等最终处理状态；`dispatch_result` 表示该 event 是否被 collector/observer 接收、拒绝或使 connection 关闭。

event 与 outcome 都只存维度和 raw 文件引用，不重复 raw payload；需要理解内容时按 `seq` 打开 raw 文件同一行。后续分析可按 `attempt_id`、`thread_id`、`turn_id`、`item_id`、`method`、`message_class` 或 `generation` 过滤 event，再按 `seq` 与 outcome join、排序还原整条时间线。例如：`jq -s 'map(select(.turn_id == "<turn-id>")) | sort_by(.seq)[]' appserver-event-*.jsonl`，再以相同 `seq` 查询 `appserver-outcome-*.jsonl`。

### 6.6 对现有 S02 回显契约的替换

`worker.PreparedAppServerRequest`、`Prepare` 和 `SimulatedProcessSeconds` 是 S02 验收辅助，S03 实现后不再是运行时协议或用户可见内容。实现将替换它们为 `codexapp` 的显式 request/response structs；配置删除模拟秒数，补充 child 启动、30 秒 RPC、500 秒 turn、90 秒 idle、10 秒 grace、per-generation replacement 与 debug event JSONL 等受校验字段。README 启动说明必须增加 Codex 登录、child `initialize` 成功、`app_server_unavailable` 的人工恢复方式和本 Story debug event 日志位置的检查；不新增 App Server readiness HTTP 门禁。

## 7. 测试设计与验收标准

| 编号 | Given / When / Then |
|---|---|
| S03-AT-00 | Given enabled App 收到有效 p2p/group text，When ingress，Then 无 Allow list 配置仍持久化并入队；group Worker key 为事件 `chat_id`，p2p Worker key 为 `sender_open_id`，两者均把事件 `chat_id` 写入同一个 ChatGroup 定位路径；topic_group 仍按既有规则忽略，且 S03 不创建 chat/sender 白名单表。 |
| S03-AT-01 | Given 无论本机是否存在外部 App Server，When runtime boot，Then 只启动一个本 bot owned stdio child、先 initialize 后启动 receiver，且仅有一个 client reader/writer；外部 PID 不在任何命令目标内，也不设 App Server readiness HTTP 门禁。 |
| S03-AT-02 | Given owned child start、stdio writer/reader 或 initialize 失败，When runtime boot，Then启动报确定性错误、receiver 未启动、失败不影响外部进程。Given running child EOF/exit，Then active Batch 只失败一次、single supervisor 为该退出 generation 只创建一个 replacement child、旧 Batch 不自动重放。Given replacement initialize 失败，Then进入 `app_server_unavailable`、不循环 restart；后续入站持久化为 failed 并收到确定性失败文本。Given replacement initialize 成功且以后再次 exit，Then连续失败链已重置，并可为这次新 exit 再创建一个 replacement。 |
| S03-AT-03 | Given 初始化完成，When 两个 goroutine 同时发 request，Then stdout response 依 id 回到各自 caller，stdin 每行一个完整 JSON，不交错。 |
| S03-AT-04 | Given ChatGroup 无 Thread ID，When Batch 开始，Then request 顺序为 `thread/start -> SetThreadIfExpected -> turn/start`；CAS 成功才使用返回 `thread.id`，turn 带正确 `cwd/model/effort/input/clientUserMessageId=attempt_id`。 |
| S03-AT-05 | Given ChatGroup 有 Thread ID，When Batch 开始，Then 先 `thread/resume`；resume 成功不 start，失败只 start 一次并 CAS 替换 ID、记录 `resume_fallback_started_new_thread`；CAS 失败时 archive 新 Thread、重读且绝不在未持久化 Thread 上 start turn。 |
| S03-AT-06 | Given S02 的多条 FIFO Batch，When 发起真实 turn，Then `input` 恰有一个 `{type:"text"}`，其 text 保持消息顺序；不出现 S02 `PreparedAppServerRequest` 字段或裸 string array。 |
| S03-AT-07 | Given 带已绑定 thread/turn 的 known server request 与无可路由身份的 unknown server request 各一例，When reader 收到它们，Then 每个都以相同 request ID exactly-once 回 `-32001 unsupported by S03`；前者只使所属 attempt 失败，后者不误伤任何 Batch，后续无关 Batch 仍可使用同一 healthy child。Given 各类 response、notification、server request 与 error，Then raw 文件对每个 server→client 行先 `Write+Sync` 再 dispatch，字节内容可与 fake 原始 JSON 精确比对。 |
| S03-AT-08 | Given notification 在 `turn/start` response 之前到达，When response 返回 `result.turn.id`，Then collector 只释放同 thread+turn 的暂存 event；duplicate/mismatch/late event 只记 debug。Given response 30 秒仍未到达，Then不猜测绑定、关闭该 generation，Batch 只失败一次。 |
| S03-AT-09 | Given 同 App 的两个 group 或 p2p channel 并发，且同一 group `chat_id` 或同一 p2p `sender_open_id` 的第二 Batch 等待，When fake server交错发送事件，Then Worker key、ChatGroup ID、Thread/Turn/Card 无跨 channel 污染，同一调度键严格等前一 completed。 |
| S03-AT-10 | Given 500 秒 total、90 秒 idle、10 秒 interrupt grace 的任一边界，或 `turn/completed.status` 为 failed/interrupted、server EOF，When 收尾，Then interrupt 至多一次；无 turn ID/grace 到期时关闭 child，所有受影响 collector 只失败一次；card/text 为确定性安全失败文案、用户消息不自动重放。Given raw debug writer 失败，Then关闭该 generation、所有 active collector 失败且 L3 证据标记 incomplete。 |
| S03-AT-11 | Given work card PATCH 失败，When真实 turn 已 completed，Then走已有 text fallback；不泄露 delta 或 JSON 请求参数。 |
| S03-AT-12 | Given真实并发 client、single supervisor replacement、worker 关闭和事件流，When `go test -race ./... -count=20`，Then无 race、死锁、pending request 泄漏、重复终态或旧 generation 关闭新 child。 |
| S03-AT-13 | Given 两个 App、两个 channel、多个 attempt/thread/turn/item 以及 response/notification/server request/error 交错到达，When reader 写入 debug 证据，Then raw/event 第 N 行与 `seq=N` 一一对应，outcome 可按同一 `seq` join；event 的时间、generation、JSON-RPC 分类、产品路由、App Server 路由和 `route_snapshot` 正确，outcome 的 `route_state/dispatch_result` 正确，按任一维度过滤并按 `seq` join/排序可恢复对应 event 时间线。Given outcome writer 失败，Then generation 关闭且证据标记 incomplete。 |

L1 覆盖 JSON-RPC classification/serialization、request correlation、raw/event/outcome writer、event dimension mapper、outcome mapper、input mapper、event collector；L2 使用 fake stdio App Server、fake storage/Feishu 覆盖 child lifecycle、Worker+DB 交互；L3 和 L4 只证明真实边界，不能由 fake 代替。

## 8. 最终本地集成校验

### S03-LI-01：真实飞书到真实 App Server 的事件观察闭环

**前置**：测试 App 已导入且 `workspace_dir` 可访问；Docker MySQL healthy；`codex login` 成功；服务配置为 `logging.level: debug`；启动前记录 `codex --version` 输出。

| 步骤 | 操作 | 预期与证据 |
|---|---|---|
| 1 | 停止旧 bot，按新配置启动服务。 | 日志含本 bot owned child 的启动 PID/版本指纹（不含外部 PID）、成功 `initialize`、新进程启动时间；`/healthz` 成功，`/readyz` 仅记录 receiver snapshot，非 App Server 健康门禁。 |
| 2 | 在仅含测试 App 的 p2p 或测试群发送唯一标识 text，例如 `S03-LI-01-<timestamp>`。 | 收到原 Batch card，不显示 S02 参数 JSON、workspace 或原始协议内容；保存飞书 bot message ID。 |
| 3 | 等待真实 turn 结束。 | 同一 message ID 的 card PATCH 为“本轮已完成”，或明确安全失败文案；不得新建一张“完成”卡片。 |
| 4 | 用 trace ID 查询日志与 MySQL。 | `appserver-raw-<process-start>.ndjson` 按收到顺序包含 `thread/start|resume`、`turn/start` response、`turn/started`、全部收到的实际 notification/server request/error、`turn/completed`（或失败根因）的完整原始 JSON 行；event/outcome 文件可按 app/channel/attempt/thread/turn/item/method/class/generation 筛选、以 `seq` join 并还原时间线。raw/event 在 dispatch 前 Write+Sync，outcome 在 dispatch 后 Write+Sync；若任一文件标记 incomplete，此次验收失败。MySQL Batch Message 同为 succeeded 或合法 failed，共用同一可见 message ID。 |
| 5 | 再发送第二个唯一输入。 | 若上一 Thread 可用，日志显示 `thread/resume`；该 input 产生新的 turn，且没有上一 input 自动重放。 |

若步骤失败，先保存 child start/initialize、完整 debug JSONL、trace、thread/turn、server exit/error、Feishu message ID、DB status 和进程启动时间；按「child 初始化 → App Server request/notification → card PATCH → MySQL 条件状态」分层定位。失败不是 Delivered 证据。

## 9. Definition of Done

- [ ] S03-AT-00 至 S03-AT-13 的实现与测试完成；`gofmt`、`go vet ./...`、`go test ./...`、`go test -race ./... -count=20` 通过。
- [ ] 真实 App Server client 是唯一连接入口，已经实现 owned stdio child、initialize-first、single-supervisor 按退出 generation replacement、成功 initialize 重置连续失败链与 `app_server_unavailable` 收尾；没有接管/kill/reuse 外部 App Server。
- [ ] `thread/start`/`thread/resume`/`turn/start` 已用当前 schema 的显式 structs；S02 参数回显和模拟 sleep 不在用户主链路。
- [ ] 每个 App Server response、notification、server request 与 error 都在 dispatch 前 `Write+Sync` 到 raw/event 文件，并在 dispatch 后写 outcome；三者以 `seq` 还原事件、路由、时间和处理结果的完整序列。server request 回同 ID 的通用 `-32001`，仅在有已绑定 route 时失败所属 attempt；`turn/completed` 驱动唯一终态，不将流式内容回吐飞书。
- [ ] Thread CAS、以 `turn/start` response 的 `turn.id` 绑定 collector、30s/500s/90s/10s 收尾、Message 状态、card/text fallback、服务退出和 resume fallback 的 MySQL/日志契约均有测试。
- [ ] README、配置模板、HLD 影响检查和运行手册同步；若实现改变本设计中的全局契约，同时更新 HLD 和本 Story。
- [ ] S03-LI-01 在新进程、真实 Codex App Server、真实 MySQL 和真实飞书上通过；保存 trace/message/thread/turn、完整 raw/event/outcome debug 文件和 DB 终态证据。
- [ ] 独立技术架构、质量/安全、产品架构评审完成；blocker 整改并复审后才从 Ready/In Development 标为 Delivered。

## 10. 风险、残留项与后续 Story

| 风险/残留 | 当前控制 | 后续责任 |
|---|---|---|
| child stdio 的 CLI/protocol 行为随版本变化 | 启动前 schema/version/initialize 探活，记录 CLI 版本；`turn/start` response 的 `turn.id` 仍是 collector 唯一绑定依据，不以旧文档假定兼容。 | 运行 SOP / 升级验证。 |
| `approvalPolicy:"never"` 下仍出现新的 server request 类型 | 统一路由、以同 ID `-32001` 通用拒绝；仅有已绑定 route 时使对应 attempt 失败，无 route 时不误伤其它 Worker。 | Approval Story 实现持久化请求和飞书回调。 |
| 个人 `all` ingress 未来被用于非个人场景 | 当前只适用于自用本地 enabled App；不把“空配置”暗改为拒绝。 | 需要限制时新开 Story，按 app/chat 维度明确实现规则。 |
| 完整 event 可能包含模型/工具内容 | 用户已明确授权 S03 的个人本地独立 debug JSONL 保存原始 event；唯一 reader 在 dispatch 前 Write+Sync，测试后关闭 debug。 | 后续流式卡片 Story 另行定义用户可见内容。 |
| child 连续启动/initialize 失败 | 每次运行中退出可创建 replacement；只有 replacement initialize 失败才标记 `app_server_unavailable`，不做连续启动重试。成功 initialize 即重置失败链。 | 人工修复 Codex 登录、CLI、配置或主机环境后重启 bot。 |
| `turn/completed` 的 usage 或未来事件字段可能与 schema 漂移 | raw event JSONL 保留实测原貌，终态使用稳定 thread/turn/status；生成 schema 是实现前门禁。 | 协议兼容/版本升级 SOP。 |
| bot restart 中断自己的 active turn | child 与 bot 同生命周期；中断 Batch 不自动重放。 | runtime/command Story：明确用户可见失败、resume fallback 与重试 UX。 |
| `/new`、`/stop`、卡片按钮、附件与 Langfuse 未实现 | 不在 S03 路径暴露这些入口；服务关闭使用安全失败收尾。 | 独立 Command、Approval、Attachment、Observability Story。 |

### 设计依据

- [Story 从设计到 Delivered](../sop/story-design-to-delivery.md)
- [Story 撰写规范](STORY_WRITING_SPEC.md)
- [Codex App Server 协议调研](../01-codex-appserver-protocol-research.md)：本机实验与事件表；S03 实现前必须再次运行 schema 生成校验。
- 本机 2026-07-11 证据：`codex app-server generate-json-schema --experimental` 成功生成 v1/v2 schema；独占 `codex app-server --stdio` 的 `initialize` 实际返回 `codexHome`、`platformFamily`、`platformOs`。
