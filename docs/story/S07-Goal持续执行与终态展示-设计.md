# S07：Goal 持续执行与终态展示

> **状态**：Delivered（2026-07-13；实现、全量门禁、真实 App Server L3 与测试飞书 L4 已完成）  
> **关联概设**：[HLD v4.5](../02-redesign-high-level.md) §2.3、§4、§5、§7  
> **依赖**：S01–S05 Delivered；现有命令实现；已登录本机 Codex App Server；测试飞书 App 的消息与 CardKit 权限。  
> **取代**：仅取代历史 S06 命令草稿中 `/goal` 的“只设置、不启动 Turn”契约；定时任务 S06 的范围不变。

## 1. 一句话目标

让飞书用户发送 `/goal <目标>` 后，Codex 立即把目标作为首个提示词开始工作，并在同一频道持续呈现可见进展与最终结果，直到 Goal 进入权威终态。

## 2. 背景

官方 Codex 语义是：`/goal` 的文本同时是首个 prompt 和任务完成条件；Goal 是 Thread 持久状态，且会在安全边界继续运行。App Server 的 `thread/goal/set` 管理的正是 TUI `/goal` 的同一份状态，而 `turn/start` 才启动生成。

现实现只执行 `thread/goal/set` 后返回“目标已设置”，随后释放 Worker；它既没有把目标作为首个 prompt 提交，也没有保留对后续 continuation Turn 的事件归属。因此用户看不到工作过程或最终交付。这是产品契约错误，而非提示文案问题。

## 3. 范围

### 3.1 本期做什么

1. `/goal <目标>` 在同频道 FIFO 中成为独占 **GoalRun**：创建或恢复目标 Thread，调用 `thread/goal/set(active)`，再用同一目标文本调用 `turn/start`。目标文本不添加 `<now>`。
2. GoalRun 持有频道执行权，跨首个 Turn 以及 App Server 自动 continuation Turn 持续关联 `threadId + turnId`；不把首个 `turn/completed` 视作完成。
3. work 模式创建一张正常流式卡：只展示 `agentMessage` 的 `commentary` 或 `final_answer`，以及本服务生成的固定工具状态摘要（类型、状态、耗时）。绝不展示 reasoning、userMessage、计划原文、工具参数/命令/cwd/查询/结果/输出。Goal 首个 prompt 直接开始卡片流，不发送“目标已设置”的静态确认。companion 模式跨所有 continuation 累积允许输出；即使首个 Turn 含 `[[SEND]]` 也不得发送或关闭，只有 Goal 权威终态才可一次性进入既有分段交付。
4. 将 `thread/goal/updated` 作为 Goal 生命周期权威事件：`complete` 关闭卡片并写最终可见结果；`paused`、`budget_limited`、外部中断或 App Server 退出以稳定终态收尾，并保留已得到的可见输出。
5. `/cancel`/`/stop` 先建立本地 stopping fence 并暂停 Goal，再中断已绑定 Turn；`/new` 仅在暂停确认且没有 current/late-bound/continuation Turn 后归档 Thread。新普通消息不得越过活跃 GoalRun；不同频道仍可并发。
6. `/goal` 输入限制为非空、最多 4,000 **Unicode rune**。目标明文不会进入 MySQL receipt、飞书固定命令回复、slog、workflow JSONL 或 debug timeline；S08 已按操作者的个人自用自托管 Langfuse 裁决，将其作为业务 input 明文写入 Langfuse。它也会按 Codex 产品语义作为 App Server Thread goal 与首个 Turn input 存在于该 Thread 的受控历史中。Thread archive 也不等于删除该受控历史；其留存、权限和访问控制由运行 App Server/Codex 账户所有者负责。

### 3.2 本期不做什么

- 不自行轮询或伪造无限 `turn/start` 循环；续跑由 App Server 的 active Goal continuation 调度，避免无工具调用时自旋。
- 不暴露隐藏 chain-of-thought。飞书只呈现现有允许的 agent 可见消息、工具/计划状态与最终答复。
- 不增加一个独立的 Goal 数据表、Redis、后台队列或每频道 App Server 进程；Thread 的 Goal 状态仍由 App Server 持久化。
- 不实现 `/goal` 的编辑、查看、恢复、清除子命令；本期只实现设置并立即启动。暂停由 `/cancel`/`/stop`，清除由 `/new` 归档 Thread。

## 4. 依赖与前置条件

| 依赖 | 状态 | 本 Story 用法 |
|---|---|---|
| S02/S04 Worker 与双区卡片 | Delivered | GoalRun 占有同频道、流式展示与控制收尾。 |
| S03 App Server Runtime | Delivered | JSON-RPC、Thread/Turn 路由、timeline。 |
| App Server Goal API | 本机已登录 | `thread/goal/set` 与 `thread/goal/updated`。 |
| 测试飞书 App | 已配置 | L4 验证流式卡和最终交付。 |

## 5. 核心设计决策

| 问题 | 结论 | 原因 | 后果 |
|---|---|---|---|
| `/goal` 是否只写状态 | set 后立即以目标文本启动首个 Turn | 官方定义目标文本同时是首个 prompt 与完成条件 | 不再有静态成功确认。 |
| 无/失效 Thread | 走与普通文本相同的安全 start/resume-fallback，并 CAS 持久化新 Thread | “立即开始”不应被旧的懒创建边界阻断 | `/new` 后的首条 `/goal` 可以工作。 |
| 首个 Turn completed | 仅表示一个 Turn 边界；GoalRun 保持活跃 | active Goal 可自动 continuation | Worker、card 与事件路由不得提前释放。 |
| 终态依据 | `thread/goal/updated` 的 terminal status；中断与进程退出是本机强制终态 | Goal 生命周期由 App Server 所有 | 不用模型输出文字猜测完成。 |
| 目标内容记录 | App Server Thread 保存；本服务所有持久化/观察面脱敏 | 同时满足 Codex 语义与本地隐私边界 | debug writer 必须过滤 goal 和首个 Turn input。 |
| 首 Turn 之前失败 | `goal/set(active)` 后进入 `goal_set_pending_first_turn`；首 Turn 未 authoritative bound 前任一失败均恰好一次 `goal/set(paused)` 补偿 | 避免 App Server 留下无人拥有的 active Goal | 暂停未知时不注销 owner、不释放频道；以幂等 pause + `goal/get` 有界确认，只有确认 paused/terminal 或该 App Server generation 已退出才可稳定显示 `goal_start_failed` 并释放。 |
| continuation 被抑制 | 不伪造 `turn/start` 循环；进入有界 `awaiting_continuation`，超时后暂停并可见收尾 | 既不空转，也不能把频道占到 90 分钟 | 使用现有 Runtime idle timeout（当前 90 秒）；错误码 `goal_continuation_suppressed`。 |

## 6. 主链路与接口契约

### 6.1 设置并启动

```
Feishu /goal objective
  -> receipt: user_content="/goal [redacted]"
  -> Channel Worker GoalRun（等待此前 Batch，阻断后来同频道 input）
  -> EnsureGoalThread：resume；无/失效则 thread/start + CAS persist
  -> create streaming delivery（已知拒绝/unknown 均不进入 set）
  -> register GoalAttempt/redaction registry(threadId, generation)
  -> thread/goal/set {threadId, objective, status:"active"}
  -> state=goal_set_pending_first_turn
  -> turn/start {threadId, cwd, input:[{type:"text", text:objective}]}
  -> authoritative first turn binding -> state=active(turn N)
```

`EnsureGoalThread` 必须使用当前 App 的 `workspace_dir`、model、approval policy、sandbox 和动态工具 catalog。线程创建的 CAS 失败时归档刚创建的 orphan Thread，重新读取指针并以稳定失败收尾；不得把同一目标分发到两个 Thread。

### 6.2 GoalAttempt、乱序与终态栅栏

```
delivery_ready --goal/set(active)--> goal_set_pending_first_turn
goal_set_pending_first_turn --first authoritative binding--> active(turn N)
active(turn N) --turn/completed--> awaiting_continuation
awaiting_continuation --turn/started(turn N+1)--> active(turn N+1)
active|awaiting --goal complete--> completing -> terminal_complete
active|awaiting --goal paused|budget_limited--> terminal_noncomplete
active|awaiting|goal_set_pending_first_turn --pause unknown--> stopping_fenced
stopping_fenced --paused|terminal|generation exit--> interrupted/failed
active|awaiting --/cancel|/new|process exit--> interrupted/failed
```

`GoalAttempt` 在 `goal/set` **之前**注册为该 `threadId` 的唯一 generation owner，并保存 generation、threadId、state、current turn、known turn 集合、event seq、terminal fence 与 stopping fence。它只接受相同 generation + threadId 的事件；`thread/goal/updated` 不依赖单一 turnId。正常情况下 `item/*` 必须匹配 current/known turn；但在一个已知 Turn `completed`、Goal 仍 active 的 `awaiting_continuation` 边界，下一条携带新 turnId 的明确 Turn 证据（完整 `turn/completed`，或具有 ID 且 type 为 `agentMessage` 的 `item/started` / `item/completed`）也可原子绑定为 continuation。这覆盖 App Server 先发 item、后发或不发 `turn/started` 的事件顺序；不使用未带 item 类型的 delta 作为绑定依据。除此边界外不在 known 集合的事件只写脱敏 timeline，绝不更新卡片、receipt 或别的频道。

首个 `turn/start` response、`turn/started`、`turn/completed`、terminal goal update 可以任意先后到达：`goal_set_pending_first_turn` 仅接受首个同 Thread 的 authoritative turn binding；response 延迟时先到的 `turn/started` 可以绑定，随后 response 仅校验一致；已到 terminal 的 response/started 不重新激活。收到某 Turn `turn/completed` 且 Goal 仍 active 时进入 `awaiting_continuation`，保留 owner 和输出，并启动一个 Runtime idle-timeout 的 continuation timer。时间内有新 `turn/started`，或有携带新 turnId 的 item/Turn 证据，则绑定；时间届满仍无新 Turn/terminal，恰好一次发起暂停，确认 paused/terminal 后显示 `goal_continuation_suppressed` 并释放频道；确认未知则保持 stopping fence fail-closed。进程退出、整体 deadline、每 Turn total/idle deadline均由同一个 terminal arbiter 先封栅栏、再暂停/中断和注销 registry。

收到 terminal `thread/goal/updated` 时，terminal fence first-wins。若 current Turn 仍未完成，等待该 Turn 的 `turn/completed` 或现有 Grace 后 `turn/interrupt`，再关闭，避免丢失末尾可见输出；若已在 `awaiting_continuation`（即已知 current Turn 已 completed），则立即关闭，拒绝之后未知 continuation 的 late item。若首 `turn/start` 是否已被服务器接受、或暂停 response 为 unknown，GoalAttempt 进入 `stopping_fenced`：保留频道执行权、owner 与 redaction registry，以幂等 `goal/set(paused)` 加 `goal/get` 做有界确认；任何普通输入只能排队不得启动。仅确认 paused/terminal，或该 App Server generation 已退出时，才显示 `goal_start_failed` / 释放频道。所有成功、失败、暂停、超时、退出、补偿和控制路径均通过同一个注销路径清除 owner/redaction registry；不得由单 Turn `finish` 直接删除 GoalAttempt。

`complete`：卡片 final zone 为最后一个 agent 可见 final answer，状态“目标已完成”。若权威 complete 未给可展示 final，则显示“目标已完成，但未收到可展示的最终摘要；请检查已完成的操作与日志”，不得生成空成功卡。
`paused`：显示“目标已暂停”，已取得内容保留。
`budget_limited`：显示“目标达到预算限制，未宣告完成”，保留模型给出的进度/下一步。
中断、超时、Runtime exit：显示稳定失败/中断说明，message receipt 标记失败，绝不虚报完成。

### 6.3 运行时边界

`Runtime.StartGoal`（名称以实现为准）与普通 `StartTurn` 共用单一 stdout reader/writer 和 App Server client，但 GoalAttempt 允许同一 Thread 的多个连续 Turn。它对 `turn/started`、`turn/completed`、`item/completed`、`thread/goal/updated` 建立显式关联；无活跃 GoalAttempt 的 continuation 事件只写脱敏 timeline，不路由到其他频道。

每次 GoalAttempt 有一个整体 deadline（沿用 Worker `ProcessTimeout`）；每个 Turn 有现有 total/idle deadline。任一局部超时中断当前 Turn，并将 Goal 标为 `paused` 后以本机失败收尾。App Server 退出时不自动重放首个 prompt；后续用户输入可触发已有 resume 语义。

### 6.4 卡片、companion 与可见性

work 模式复用 S04 的 CardKit 全量实体更新。Goal 新卡首先显示固定生命周期状态“目标已受理，正在启动 Codex…”，不以“等待 Codex 进展…”伪装为已发生的模型输出。Projection 白名单仅为 `agentMessage` 的 `commentary`、`final_answer`，以及本服务生成的工具摘要（固定字段：工具类型、状态、耗时）；它拒绝 reasoning 的全部类型、userMessage、plan 文本、工具参数/命令/cwd/查询、结果/输出、动态/MCP payload。每 250ms 刷新；完成时只关闭一次。若终态没有 allowlisted commentary，progress 区固定显示相应的生命周期状态：complete 为“目标已完成；本轮未收到可展示的阶段进展。”，paused、budget_limited、timeout、cancelled 和其它失败分别显示其明确未完成状态；final 区仍显示可见 final 或安全终态摘要，绝不留下“等待”假象。CardKit 明确拒绝时遵循现有一次 plain-text fallback；unknown 不重发，且 final-card 的 rejected/unknown 在终态 receipt 中使用稳定交付错误码。

companion 模式保持 `[[SEND]]` 和 DeliverySlot 规则，但 GoalAttempt 在终态前只累积，不解释 marker、不发送分段、不完成 lifecycle。terminal complete 时将所有 Turn 的最后允许 final 交给一次既有交付；paused/budget/失败只发送一条固定安全终态说明，绝不把半段内容当成功交付。

### 6.5 控制、并发和数据

`/cancel`/`/stop` 通过现有 Worker control barrier：先设置 GoalAttempt 的 stopping fence，调用 `thread/goal/set {status:"paused"}`，以 response 的 goal status 或 `thread/goal/get` 确认暂停，再中断 current/late-bound Turn。暂停 unknown 时保持 fence、owner 与频道 fail-closed，按有界重试 pause/get；只有 paused/terminal 确认或 App Server generation 已退出才收尾并允许后续输入，绝不把未知暂停伪装成安全取消。`/new` 在暂停确认、Turn drain/Grace 完成且 registry 注销后才 archive Thread/CAS clear；不得 archive 仍可续跑的 Thread。awaiting、terminal-drain、process-exit 各状态的控制均走同一 fence；之后到达的旧事件仅进入脱敏 timeline。

`messages` 继续保存 `/goal [redacted]`、effect/reply outcome、终态错误码、bot message id、整体 elapsed；不增加目标明文字段。现有普通 batch lifecycle 对 GoalRun 需要可表达“持续中/终态”的条件更新，避免首个 Turn completion 把 receipt 写为 succeeded。

## 7. 测试设计与验收标准

| 编号 | Given / When / Then |
|---|---|
| S07-AT-01 | Given existing Thread and `/goal sentinel`, When GoalRun executes, Then fake App Server observes `resume → goal/set(active) → turn/start(input=sentinel)`，没有静态 command success。 |
| S07-AT-02 | Given no Thread or resume failure, When `/goal`, Then create/replacement Thread 被 CAS 持久化，目标仍启动一次；CAS 失败 archive orphan，绝不双发。 |
| S07-AT-03 | Given first turn completed、active goal、auto continuation with a new turn id、second final item、goal complete, When Runtime routes events, Then同一 GoalAttempt 接收两轮可见输出且只在 complete 后返回；覆盖 `turn/started` 与其缺失时 item-first continuation、terminal 先/后 turn completion、延迟 `turn/start` response 与重复/旧 generation event。 |
| S07-AT-04 | Given goal complete/paused/budget_limited/interrupted/runtime exit/start failure/continuation silence, When terminal sequence arrives, Then每个状态有唯一终态、card 文案和 receipt 状态；非 complete 不得写“已完成”。覆盖“turn/start 已接受但 response 丢失 + pause response 丢失”：保留 owner/频道、普通 Turn 不启动，直到 paused/terminal 或 generation exit。 |
| S07-AT-05 | Given two channels plus active GoalRun A, When normal message B and A 的普通消息/`/cancel`/`/new` 到来, Then B 可并行；A 的普通消息不越过 GoalRun，control 先暂停、drain 后才 archive，晚到事件不更新输出。 |
| S07-AT-06 | Given work/companion、CardKit failure/rejection/unknown and injected reasoning/user/plan/tool command/output/MCP payload, When Goal streams, Then卡片、fallback、MySQL 与 timeline index 只含白名单内容；companion 在 terminal 前不发送 marker segment，终态交付遵循一次 fallback/unknown 不重发。 |
| S07-AT-07 | Given real JSON-RPC `thread/goal/*` objective 与首个 Goal userMessage/text sentinel, raw timeline/MySQL/log/Langfuse/workflow fakes, When whole path executes, Then objective 在 S08 Langfuse self-hosted Project 明文可检索；其余 bot-owned receipt/log/workflow/timeline 表面仍无明文，未知/不能判定的 Goal raw envelope fail closed 不落盘。 |
| S07-AT-08 | Given real local Codex app-server and a bounded objective that creates a file then verifies it, When `/goal`, Then记录至少一个 Goal state update、首 Turn、（若服务器产生）continuation Turn、最终 goal status、thread/turn ids 和脱敏 timeline。 |

L1：Goal status mapper、event correlation、redactor；L2：Runtime fake、Processor/Worker/Router/Feishu fake 和 race；L3：真实 App Server opt-in；L4：测试飞书 App 在新 bot process 上观察 `/goal` 的运行卡、进展和终态。所有并发改动运行 `go test -race`。

## 8. 最终本地集成校验

**S07-LI-01：飞书 Goal 从启动到终态**

前置：新二进制、新进程、当前 MySQL migration/state、`/healthz=ok`、receiver connected、Codex 已登录、测试飞书 App 有 CardKit 权限。

1. 在测试 p2p 发送唯一 `/goal 在当前 workspace 创建 goal-smoke-<nonce>.txt，写入 nonce，读取验证内容，完成后报告验证证据`。
2. 观察同一条 work 卡从“生成中”出现可见进展，再以“目标已完成”关闭；不得先收到“目标已设置”的静态卡。
3. 保存 card/message 的脱敏标识、trace、thread id、所有 turn id、goal terminal status、MySQL receipt 和新进程后的 timeline index；确认文件存在且 nonce 一致。
4. 另起一次长 Goal 后发送 `/cancel`，确认卡为“目标已暂停/中断”而非“完成”，且随后普通文本仍在同一频道可处理。

失败先检查新进程 PID/启动时间、App Server availability、脱敏 timeline 的 `thread/goal/*`、Worker channel state 与 message terminal state；不得以旧进程或 fake 绿灯替代 L4。

**2026-07-13 L4 结果**：测试群组实际 `/goal` 在 `19:44:33` 创建 CardKit 卡，运行中于 `19:44:44` 和 `19:45:03` 两次全量更新（`closed=false`），`19:45:04` 以终态更新关闭并完成 batch。用户确认本次展示正常；这证明可见 item 已进入双区卡片 projection，而非旧版“只收到 Goal terminal”的兜底路径。

## 9. Definition of Done

- [ ] Story List、HLD、协议调研与本 Story 对 `/goal` 的“目标即首 prompt + completion criteria、set 后立即启动、active 时续跑”一致；历史 S06 命令草稿明确链接本 Story。
- [ ] Router、Worker、Processor 与 Runtime 实现 GoalRun/GoalAttempt；无/失效 Thread 可启动、连续 Turn 不丢失、终态唯一、取消/new 串行正确。
- [ ] objective 在 receipt/log/timeline/workflow 中全程脱敏，但在 S08 self-hosted Langfuse Project 明文记录；可见卡不回显目标本身，除非模型在正常最终答复中主动引用。
- [ ] Goal raw event 在写入 timeline 前由预注册 thread→Goal redaction registry 消毒：覆盖 `params/result.goal.objective`、Goal 首 Turn 的 `userMessage.content[].text`，保留 digest/byte length；Goal 相关未知 envelope 不写原始文件。
- [ ] pause/get 结果 unknown 时 GoalRun fail-closed：保留 stopping fence、registry 与频道执行权；只有 paused/terminal 确认或 App Server generation exit 才可注销并接受普通 Turn。
- [ ] S07-AT-01 至 AT-07、`gofmt`、`go vet ./...`、`go test ./... -count=1`、相关 `go test -race` 有当前证据；L3/L4 结论不混淆。
- [ ] 运行时代码变更已用新二进制/新进程应用，health/receiver/关键数据库状态 read-back 已完成；服务保持运行等待 LI-01 的人工飞书验证。

## 10. 风险、残留项与后续 Story

| 风险/残留 | 当前控制 | 后续责任 |
|---|---|---|
| App Server continuation 事件形状随版本变化 | 用真实 L3 schema/event evidence 锁定；未知 event 不跨频道路由 | Protocol compatibility gate。 |
| 无工具的 continuation 被 Codex 抑制 | 不伪造循环；90 秒无 continuation 后暂停并明确告知用户 | Goal UX/explicit resume Story。 |
| 长 Goal 占用频道 | 这是 Goal 的串行语义；用户可 `/cancel`，不同频道并行 | 后续多任务/后台执行产品设计。 |
| 目标在 App Server Thread 受控历史内 | 必须如此才能成为 first prompt；bot-owned traces 严格脱敏 | Thread retention/data controls Story。 |

### 设计依据

- [Codex Long-running work: Start a goal](https://learn.chatgpt.com/docs/long-running-work#start-a-goal)
- [Codex App Server: Manage a thread goal](https://learn.chatgpt.com/docs/app-server#manage-a-thread-goal)
- [Using Goals in Codex](https://developers.openai.com/cookbook/examples/codex/using_goals_in_codex#how-goals-are-designed-in-codex)
- [Story 从设计到 Delivered](../sop/story-design-to-delivery.md)
