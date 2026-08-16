# Codex Workspace Bot 概要设计 v4.6

> **版本**: v4.6 | **日期**: 2026-07-14
> **定位**: 个人本机飞书 AI 工作区编排器 — Codex 原生版本
> **技术栈**: Go 1.23 + Codex App Server + MySQL + 飞书 SDK + Langfuse

---

## 目录

1. [设计目标](#1-设计目标)
2. [核心实体模型](#2-核心实体模型)
3. [系统架构](#3-系统架构)
4. [消息处理流程](#4-消息处理流程)
5. [Codex App Server 集成](#5-codex-app-server-集成)
6. [流式输出与飞书卡片](#6-流式输出与飞书卡片)
7. [命令系统](#7-命令系统)
8. [Workspace 管理](#8-workspace-管理)
9. [数据层设计](#9-数据层设计)
10. [配置体系](#10-配置体系)
11. [可观测性](#11-可观测性)
12. [功能清单](#12-功能清单)

---

## 1. 设计目标

### 1.1 核心原则

| 原则 | 说明 |
|------|------|
| **Codex 原生** | 只做 Codex 版本，不做 Claude 兼容，不做引擎抽象 |
| **单 App Server** | 一个 App Server 进程服务所有 workspace；Docker 发行可跨容器，但不能变成多实例或共享 daemon |
| **简化存储** | MySQL 持久化 + 内存队列，不用 Redis |
| **按模式输出** | work 用 Notification 驱动流式卡片；companion 只在成功终态后以 final answer 多条文本送达 |
| **编排层可观测** | Langfuse 直接接入 App Server Notification，不用 hooks |

### 1.2 不做的事

| 不做 | 理由 |
|------|------|
| Engine 抽象接口 | 只做 Codex，过度抽象是浪费 |
| 渐进式迁移 | 不打算再用 Claude Code |
| Redis | 单体进程，内存队列足够 |
| SQLite/PostgreSQL | MySQL 更好管理 |
| per-engine 配置 | 第一期只支持 Codex 原生模型 |

---

## 2. 核心实体模型

### 2.1 持久化关系

```text
apps（飞书应用与运行策略）
  1 └── N chat_groups（一个 App 下的单聊或群聊，一个当前 Codex Thread）
              1 └── N messages（一次用户请求及其最终结果，即一轮）
              1 └── N scheduled_tasks（S06：绑定 owner 的 Cron 定义）
                          1 └── N scheduled_task_runs（每个已领取的时间 slot）
                                      1 └── N scheduled_task_deliveries（每次自动卡片或 fallback 交付）

Worker / 队列 = 纯内存调度实体，不持久化。
```

### 2.2 App、ChatGroup 与 Message

`apps` 是应用级配置唯一来源：名称、明文 Feishu App ID/Secret、绝对 `workspace_dir`、`work|companion`、模型、`reasoning_effort`、启用状态。机器为个人自用，App Server 固定使用 `approvalPolicy=never` 和 `sandbox=danger-full-access`，不做每 App sandbox 策略。**已裁决：当前 ingress mode 为 `all`**；任何 enabled App 收到的有效 p2p/group text 都可处理，不实现 `AllowedChats` 或自动登记白名单。若以后需要限制，规则归属是 `(app_id, chat_type, chat_id)` 的会话访问，而不是当前 Worker 的 p2p `open_id` 调度键；按人限制须另设 sender 规则。

`chat_groups` 是概设中的 Channel：其唯一身份是 `(app_id, chat_type, chat_id)`，因此同一飞书群中不同 App 的 Bot 会拥有不同 ChatGroup 和不同 Codex Thread。`chat_id` 始终保存事件的 `message.chat_id`；单聊发送目标另存为消息发送者 `open_id`，群聊发送目标为 `chat_id`。一期直接忽略 `topic_group`。

`chat_groups.codex_thread_id` 是当前可续接的 App Server Thread；`/new` 将它清空，下一条普通消息懒创建新 Thread。没有单独 Session 表。S06 增加持久 `codex_tool_catalog_version`、`catalog_upgrade_state`、`catalog_upgrade_from_thread_id` 与 `catalog_upgrade_target`：旧 catalog 只能在空闲边界经 `archive_pending → start_pending → stable` 的持久状态机升级，崩溃恢复不会 resume 旧 Thread；`schedule_enabled` 是频道 route 的持久撤销开关。

`messages` 一行是一轮，保存用户内容、最终结果、飞书 event/user/bot message ID、发送者 open_id、状态、耗时与 Langfuse 兼容 Trace ID。`trace_id` 为 32 位小写十六进制：以 `app_id + feishu_event_id` 为 seed 计算 SHA-256 并截取前 16 bytes；这既满足 W3C/Langfuse Trace ID 格式，也使同一飞书重投保持同一关联 ID。

### 2.3 Worker、队列与命令

Worker 的持久化归属仍是 ChatGroup，但内存调度 key 必须与飞书回复身份一致：group 为 `group:{chat_id}:{app_id}`，p2p 为 `p2p:{sender_open_id}:{app_id}`。每个 Worker 拥有一个内存 FIFO；普通用户消息只做最小验证和持久化后入队，Worker 每次读取当前已积累的普通消息并合并为一次 Agent 处理，保持同一调度 key 的上下文严格串行。不同 key 可并行。单聊的 `chat_id` 继续只用于 ChatGroup 持久化，不能替代 p2p Worker key 或回复目标。S06 增加 memory-only、redacted `ActorPrincipal`：普通 Batch 只能合并连续同 actor 输入；Actor 切换和 scheduled Prompt 均为 Batch 边界，以便 `schedule` tool 永远从 exact attempt owner 推导而非猜测群成员。

Router 在持久化 receipt 并完成 event-id 去重后分类命令。支持的产品命令是 `/new`、`/cancel`、`/status`、`/goal <目标>`、`/help`；`/stop` 只是 `/cancel` 的兼容别名。未知 slash 命令和错误参数持久化为命令 receipt 并返回固定错误，绝不进入普通 Turn。

`/new` 与 `/cancel` 经 Worker-owned control API 线性化：它在同一频道的 mutex 下建立 barrier、分离等待 FIFO、条件失败被移除的 receipt、取消 active turn/companion delivery，并在权威终态后发送命令结果。`/new` 还会先 archive 当前 Thread、再 CAS 清空持久化 thread pointer；下一条普通文本才懒创建 Thread。`/goal <目标>` 是同一 Worker 中的独占 GoalRun：交付可见性准备完成后，预注册 thread 级 Goal owner/redaction registry，目标文本写入 `thread/goal/set(active)`，并作为首个 `turn/start.input`，所以会立即启动；无/失效 Thread 走安全 start/resume-fallback 并 CAS 持久化。GoalRun 持有频道直到 `thread/goal/updated` 的权威终态，连续 continuation Turn 的新 turn ID 继续路由到同一 generation 输出与控制所有者；首个 `turn/completed` 不代表完成。已完成 Turn 后处于 awaiting-continuation 时，新 turn ID 可以由 `turn/started` 或首条明确 item/Turn 事件绑定，避免 App Server 的 item-first continuation 丢失可见输出。Goal 卡先显示固定“已受理/正在启动”状态；终态若无 allowlisted commentary 则显示明确生命周期状态，而不遗留“等待 Codex 进展”。awaiting continuation 在既有 Runtime idle timeout 内无新 Turn/terminal 时暂停 Goal，以 `goal_continuation_suppressed` 收尾，不伪造循环。`/cancel`/`/stop` 先建 stopping fence 并确认暂停，再中断/Drain Turn；pause/get unknown 时保留 owner、registry 与频道 fail-closed，直到 paused/terminal 确认或 App Server generation exit，`/new` 仅在此后 archive。`/status` 是 receipt-first 的只读例外：它直接并发读取账户 rate limit 与 usage，不进入 Worker，因而可先于同频道 control reply 完成。所有命令结果和 effect 分开持久化，未知发送绝不重试可见消息。

普通文本在 Router receipt 时捕获 UTC 时间，写入 `messages.received_at`；Worker 以固定 `Asia/Shanghai` 将每条文本格式化为 `<now timezone="Asia/Shanghai">RFC3339</now>` 加原始正文。`/goal` 的 objective 不使用该 formatter，却作为首个 prompt 与 completion criteria；bot-owned MySQL、日志、workflow 和 debug timeline 继续遵循各自的脱敏契约，**个人自用的自托管 Langfuse Project 是明确例外：S08 写入完整明文业务 payload 与真实业务 ID**。目标限制以 4,000 Unicode rune 校验。App Server Thread/其 archive 的留存和访问控制不由 bot 改写，归运行账户所有者负责。

默认调度值：最大活跃 Worker 20、单 Worker 队列深度 64、仅 `Idle` 状态空闲 30 分钟回收、`InProcess` 最长 90 分钟并以 10 秒优雅停止期收尾。Worker/队列属于后续 Story；Story 1 不实现它们。

---

## 3. 系统架构

### 3.1 整体架构

```
┌─────────────────────────────────────────────────────────────────┐
│                       接入层 (Ingress)                           │
│                                                                  │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────┐   │
│  │ Feishu WS    │  │ HTTP /health │  │ /debug/*             │   │
│  │ × N Apps     │  │              │  │                      │   │
│  └──────┬───────┘  └──────────────┘  └──────────────────────┘   │
└─────────┼───────────────────────────────────────────────────────┘
          │
┌─────────▼───────────────────────────────────────────────────────┐
│                       路由层 (Router)                            │
│                                                                  │
│  app_id → App config → channel_key → Worker pool → Worker       │
│                                                                  │
└─────────┬───────────────────────────────────────────────────────┘
          │
┌─────────▼───────────────────────────────────────────────────────┐
│                 调度层 (ChatGroup Worker)                         │
│                                                                  │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Worker × 20 (max)                                          │ │
│  │   ┌─────────┐  ┌─────────┐  ┌─────────┐                   │ │
│  │   │ Worker  │  │ Worker  │  │ Worker  │  ...              │ │
│  │   │ queue64 │  │ queue64 │  │ queue64 │                   │ │
│  │   └────┬────┘  └────┬────┘  └────┬────┘                   │ │
│  └────────┼────────────┼────────────┼────────────────────────┘ │
└───────────┼────────────┼────────────┼──────────────────────────┘
            │            │            │
┌───────────▼────────────▼────────────▼──────────────────────────┐
│                     引擎层 (Codex)                              │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │ Codex App Server（单进程）                                │   │
│  │                                                           │   │
│  │  Thread A (cwd=/ws/investment, model=gpt-5.5)            │   │
│  │  Thread B (cwd=/ws/health, model=codex-mini)             │   │
│  │  Thread C (cwd=/ws/course, model=gpt-5.5)                │   │
│  │  Thread D (cwd=/ws/companion, model=codex-mini)          │   │
│  │  ...                                                      │   │
│  │                                                           │   │
│  │  认证: ~/.codex/auth.json (refresh_token 自动续期)         │   │
│  │  指令: CODEX_HOME/AGENTS.md + {cwd}/CLAUDE.md             │   │
│  └──────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
            │
┌───────────▼────────────────────────────────────────────────────┐
│                       支撑层                                    │
│                                                                  │
│  ┌────────┐  ┌──────┐  ┌──────┐  ┌────────┐  ┌────────────┐   │
│  │ Config │  │MySQL │  │ Task │  │Workspace│  │ Langfuse  │   │
│  │ Viper  │  │      │  │Cron  │  │ Init   │  │           │   │
│  └────────┘  └──────┘  └──────┘  └────────┘  └────────────┘   │
└─────────────────────────────────────────────────────────────────┘
```

### 3.2 进程模型

```
codex-workspace-bot (Go 主进程)
  │
  ├── 飞书 WS Client × N (每个 App 一个 goroutine)
  ├── HTTP Server (/health, /readyz)
  ├── Worker Pool (max 20)
  │     └── 每个 Worker 一个 goroutine
  ├── Task Scheduler（MySQL task/run claim + Cron parser）
  ├── Cleanup Cron
  │
  └── Native：子进程 codex app-server --stdio
      Docker：子进程 codex-remote → byte-transparent bridge
        └── 独立 Codex 容器中的唯一 codex app-server --stdio
            └── 单进程服务所有 workspace
```

**已裁决（2026-07-11）**：App Server 是本 bot 进程独占并管理的 stdio child。服务启动时创建 child，建立唯一 client 后必须先完成 `initialize` 才进入 ready；服务退出时关闭该 child。运行中 child 退出时，单一 supervisor 标记该 generation 的 in-progress turn 失败并立即创建一个 replacement child；replacement 的启动或 `initialize` 失败时进入 `app_server_unavailable`、停止这一次连续恢复，待人工修复后重启 bot。成功 initialize 的 replacement 会重置连续失败链，以后发生新的运行中退出仍可再次 replacement。绝不自动重放已中断消息。不得探测、复用、终止或重启其它进程的 App Server，也不使用 shared daemon/proxy 作为本产品默认路径。

**Docker 发行补充裁决（2026-08-16）**：上面的“独占、唯一、由 Bot 生命周期管理”合同不变，但为了避免 `danger-full-access` Agent 继承或读取 Bot Secret，允许 Bot 的 stdio child 是最小环境的 `codex-remote`。它经隔离 control network 连接独立 Codex 容器中的 byte-transparent `codex-bridge`，由 bridge 独占拉起唯一 App Server。bridge 不理解 JSON-RPC、不持久化、不复用外部 daemon、拒绝第二个并发 client；Bot 的 `initialize`、generation、恢复和“不自动重放”语义全部保持不变。Provider API Key 另由只连接 model network 的固定上游 proxy 持有；它不是 App Server proxy，Codex 只看到无价值的占位 token。Bot、Codex、Provider proxy、MySQL 四个服务不得互相继承整套环境或挂载 `.secrets`。

---

## 4. 消息处理流程

### 4.1 标准消息处理

```
飞书 WS 事件
  │
  ▼
Receiver.parse()
  ├─ 识别 receiver App，并校验 event.header.app_id
  ├─ 解析消息类型；topic_group 直接忽略
  ├─ p2p/group 规范化为 app_id + chat_type + chat_id；另保留 p2p sender_open_id
  └─ 短事务：upsert ChatGroup、按 app_id + event_id 去重、创建 Message
  │
  ▼
Router.dispatch(msg)
  ├─ command → Worker 控制面直接生效
  └─ normal message → group(chat_id) 或 p2p(open_id) 对应的 App-isolated Worker FIFO
  │
  ▼
Worker.process(msg)
  ├─ drain 并 merge 当前普通消息
  ├─ ensureThread()；以 chat_group.codex_thread_id 续接
  ├─ Engine.startTurn({
  │     threadId: chatGroup.codexThreadID,
  │     cwd: app.WorkspaceDir,
  │     input: [merged text], model: app.Model, effort: app.ReasoningEffort,
  │   })
  └─ 处理事件流（按 workspace_mode 分流）
      ├─ work mode → turn/start 前发送初始双区卡片（正文“思考中”）
      │   ├─ item/completed.agentMessage → commentary 投影进展、final_answer 投影正文，并节流更新同一张卡
      │   └─ terminal → 最终 PATCH/关闭流式，更新同批 Message
      └─ companion mode → 不发送占位卡、不展示中间 event
          ├─ item/completed.agentMessage.final_answer → 仅暂存 final 候选
          ├─ TerminalArbiter + DeliverySlot → 唯一裁决 completed/timeout/runtime exit/control cancel；取消可阻止尚未发布的 delivery
          └─ successful terminal → 持久化 batch marker → `[[SEND]]` 分段并顺序发送 plain-text 多消息；delivery slot done 后才释放同 channel
```

### 4.2 串行与并行

```
同一 App 下:
  Channel A (群聊 1) → Worker A → Thread A  ←── 串行
  Channel B (群聊 2) → Worker B → Thread B  ←── 串行
  Channel C (单聊)   → Worker C → Thread C  ←── 串行

不同 Channel 的 Worker 并行工作 ↑↑↑
同一 Channel 内消息严格串行 ↓↓↓
```

### 4.3 超时与流式中断

超时分层，不能以单个 HTTP timeout 代替长 Turn 管理：

| 边界 | 默认 | 到期动作 |
|---|---:|---|
| 飞书事件最小入站处理 | 2 秒以内 | 未能持久化即返回错误让飞书重投；不等待模型或卡片发送 |
| App Server 普通 JSON-RPC 请求响应 | 30 秒 | 覆盖 initialize/thread/start/resume/turn/start/interrupt 的 response；`turn/start` 只以 response 返回的 `turn.id` 绑定本地 collector，不覆盖完整 Turn |
| 单 Turn 总时限 | 3000 秒（50 分钟） | 发送 `turn/interrupt`，等待最多 10 秒完成通知，再标记 `turn_timeout` |
| 流式无进展时限 | 3000 秒（50 分钟） | 若 50 分钟没有任何 App Server 事件/文本 delta，发送 `turn/interrupt`；不是卡片 PATCH 的网络超时 |
| 飞书卡片更新 | 10 秒/次 | 失败记录并降级为最终文本；不取消仍在运行的 Turn |

流式 Turn 的“超时”不是被动等待：Worker 主动 `turn/interrupt`，由 attempt-owned TerminalArbiter 以 first-CAS-wins 裁决终态；控制面 cancel/new 优先于并发 completed。companion attempt 创建时一并创建 DeliverySlot；控制面在 successful Terminal 后、Handle 发布前仍可置 cancel latch 并阻止首段发送，发布后取消未完成 delay/API/retry 并等待 slot done，才释放 Worker 或处理 `/new`。若 interrupt 请求本身超时，仍标记本地轮次超时。下一条普通消息按 Thread 恢复策略继续。以上属于整体概设，非 Story 1 实现范围。

---

## 5. Codex App Server 集成

### 5.1 认证机制

**结论：登录一次即可长期使用。**

- 登录方式: `codex login`（ChatGPT OAuth）
- 凭证存储: `~/.codex/auth.json`
- 包含: `access_token` + `refresh_token`
- App Server 启动时读取 auth.json
- access_token 过期时自动用 refresh_token 续期

### 5.2 App Server 启动

```go
cmd := exec.CommandContext(ctx, "codex", "app-server", "--stdio")
cmd.Env = []string{
    "PATH=" + os.Getenv("PATH"),
    "HOME=" + os.Getenv("HOME"),
    "LANG=" + os.Getenv("LANG"),
    // CODEX_HOME 默认为 ~/.codex，无需显式设置
}
```

该 child 的 stdin/stdout 只由本 bot 的唯一并发安全 client 持有：一个串行 writer、一个 reader 和按 JSON-RPC request ID 的 response correlation。启动不能仅以进程存活为准，必须以该连接的 `initialize` response 为 ready 证据；外部已有的 stdio App Server 没有可安全接管的 transport，不属于复用候选。

Native 模式继续直接执行 `codex`；只有设置 `CODEX_CHILD_ENV_ALLOWLIST` 时才过滤 child 环境，以免破坏现有个人本机用法。Docker 发行必须设置白名单，并把 command 指向 `codex-remote`。Codex 容器不加载 Bot、飞书、数据库、HMAC 或 Provider Secret；release canary 必须从真实飞书消息触发 Shell 检查，证明环境、`/proc`、挂载和 Workspace 均无法读到这些 Secret。

### 5.3 指令加载

**关键发现**: Codex 支持 `project_doc_fallback_filenames = ["CLAUDE.md"]`

```
现有 workspace 无需改动！
  │
  ├─ CODEX_HOME/AGENTS.md     ← 全局层（可选，默认 ~/.codex/AGENTS.md）
  │
  └─ {cwd}/CLAUDE.md          ← 项目层（现有文件直接生效！）
```

不需要 AGENTS.md 桥接，现有 CLAUDE.md 直接被 Codex 加载。

### 5.4 关键 JSON-RPC 方法

| 方法 | 说明 | 参数 |
|------|------|------|
| `initialize` | 握手 | clientInfo, capabilities |
| `thread/start` | 创建线程 | cwd, model, approvalPolicy |
| `thread/resume` | 恢复线程 | threadId, cwd |
| `turn/start` | 启动 turn | threadId, input, cwd, model, effort |
| `turn/interrupt` | 中断 turn | threadId, turnId |
| `turn/steer` | 转向 | threadId, input |
| `account/rateLimits/read` | 查套餐 | — |
| `account/usage/read` | 查用量 | — |

### 5.5 需要解析的 Notification Events

| 事件 | 用途 | 优先级 |
|------|------|--------|
| `turn/started` | 关联活跃 Turn，供超时与终态收尾 | P0 |
| `item/completed`（`item.type=agentMessage`） | **S04 用户展示来源**：work 中 `commentary` 更新进展区、`final_answer` 更新正文；companion 只暂存 `final_answer` 至成功终态；未知 phase 不展示 | P0 |
| `item/agentMessage/delta` | 仅作运行进展/idle 计时与本地诊断；不直接回吐飞书 | P1 |
| `turn/completed` | 结束 + Token 统计 | P0 |
| `error` | 错误处理 | P0 |
| `item/commandExecution/requestApproval` | 审批流 | P0 |
| `item/fileChange/requestApproval` | 文件变更审批 | P0 |
| `item/permissions/requestApproval` | 权限审批 | P0 |
| `thread/tokenUsage/updated` | Token 用量（Langfuse） | P1 |
| `item/reasoning/textDelta` | 推理过程，本项目不展示给用户 | P2 |
| `hook/started` / `hook/completed` | Hook 执行（Langfuse） | P2 |

### 5.6 可忽略的 Notification Events

| 事件 | 理由 |
|------|------|
| `thread/started` | 不需要，thread/start 已同步返回 |
| `thread/name/updated` | 飞书场景不需要 |
| `thread/goal/*` | GoalRun 必须按 threadId 路由，并驱动权威终态；不得按单一 turnId 丢弃 |
| `thread/settings/updated` | 不关心 |
| `skills/changed` | 不关心 |
| `fs/changed` | 不关心 |
| `process/outputDelta` | 命令输出已在 item/commandExecution/outputDelta |
| `account/updated` | 登录态不需要关心 |
| `model/rerouted` | 日志记录即可 |
| `warning` / `guardianWarning` | 日志记录 |
| `thread/realtime/*` | 语音暂不支持 |

### 5.7 优雅中断

```go
// /cancel 或 /stop 命令
func (w *Worker) handleCancel(ctx context.Context) error {
    if w.activeTurnID == "" {
        return nil  // 无活跃 turn
    }
    
    _, err := w.engine.Request(ctx, "turn/interrupt", map[string]any{
        "threadId": w.session.ThreadID,
        "turnId":   w.activeTurnID,
    })
    
    w.activeTurnID = ""
    return err
}
```

### 5.8 Thread 恢复容错

```go
func (e *CodexEngine) ensureThread(ctx context.Context, req TurnRequest) (string, error) {
    if req.ThreadID == "" {
        return e.startThread(ctx, req)
    }
    
    // 尝试 resume
    resp, err := e.Request(ctx, "thread/resume", map[string]any{
        "threadId": req.ThreadID,
        "cwd":      req.WorkspaceDir,
    })
    if err == nil {
        return resp.ThreadID, nil
    }
    
    // Resume 失败 → 新建
    log.Warn("thread resume failed, starting new thread", "error", err)
    return e.startThread(ctx, req)
}
```

---

## 6. 流式输出与飞书卡片

### 6.1 Work 模式流式卡片

```
S04 双区投影：
  消息 → 同一张 CardKit 流式卡（正文："思考中…"；进展：等待）
  item/completed(agentMessage, commentary) → 追加完整 text 到进展区
  item/completed(agentMessage, final_answer) → 完整 text 写入正文区
  turn/completed → flush、关闭 streaming_mode、更新为稳定终态

更新策略:
  - 每个合格 Item 都进入同一 Worker 所有的 Projection，按 item.id 去重和到达顺序累计
  - 进展更新每 250ms 合并一次，最终正文和终态立即 flush
  - CardKit 以同一 `card_id` 的全量 `Card.Update(card_json)` 更新双区；单次 entity update 失败只对该次操作降级为同一 `message_id` PATCH，不永久关闭后续 CardKit 更新
```

### 6.2 Companion 模式终态分段

```
Turn 运行中：
  不创建卡片、不发“思考中”、不展示 commentary/delta
  只暂存 item/completed.agentMessage.phase=final_answer

successful turn/completed：
  final answer → 兼容 delimiter lexer → `[[SEND]]` 语义分段 → 打字延迟 → 按序发多条 plain-text 消息

  不设总分段/总时长限制；同 channel 保持串行，直到发送完成或被控制面取消

失败/中断/超时/无 usable final（含 marker-only 零段）：
  不发送已暂存 final；只发一条确定性安全文案
```

`[[SEND]]` 是 companion 的显式分段符，不会出现在飞书消息中。为兼容模型不稳定输出，Lexer 在受保护的 code/转义区域外识别双括号的大小写、空格、全角/方头括号变体；单括号 `[SEND]` 仅能作为独立控制行生效。它生成 per-Batch internal token，并把该 token（而非 `[[SEND]]`）传给旧 Segmenter，避免 code/转义中的字面 marker 被二次拆开。它不猜测自然语言“发送/分段”，并保留 code/转义中的 marker 为正文；反斜杠按奇偶识别转义，code wrapper 必须配对。不同 final Item 在 Terminal 前 latest non-empty wins，同 item ID 去重。之后的无标记 fallback、常规 80 rune 分段目标（fallback 超过 3 段时退回原文单段）、短段合并、分段间打字延迟和逐段错误继续均兼容旧 CC Workspace Bot；本地个人模式不增加共享限流或 429 自动重试，所有出站必须发生在 Terminal drain 后。

companion 的 `messages.status=succeeded` 只说明 Codex Turn 成功；`feishu_bot_message_id` 只保存首个成功分段。第一条可能可见的 SendText 前，`MarkCompanionDeliveryStarted` 必须以单一 transaction 为全部 source Message 写入相同的不可变 `companion_delivery_batch_id` 与 `delivery_stage=companion_delivery_started`；Worker 内的 `TerminalArbiter` 以 first-wins 固定取消、失败或完成原因，`DeliverySlot` 在 marker 后才发布可取消的发送 context，且 `/cancel`/`/stop` 必须等待 slot done。`CompleteBatch` 对同一 batch/stage 的全部 processing Message 使用单一 MySQL transaction 条件收尾并清除 stage；marker 已写入后的控制取消、trace writer 或本 generation delivery failure 则必须调用 `FailCompanionDelivery`，以同样的 batch/stage 条件 transaction 将全部 source Message 迁移为 failed、写稳定错误码并清除 stage。两个 finalize API 的 DB retry 都只重试 transaction，绝不重发飞书消息；只有最终无法 fail-finalize 且 generation 退出的 marker 行才可留给 restart reconciliation。若部分/全部分段最终发送失败，分别写 `error_code=companion_segment_delivery_partial` / `companion_segment_delivery_none`；unknown send 不重试当前段、停止余段并写 `companion_segment_delivery_unknown`。全部 segment 事实由可失败 workflow JSONL writer 的 `batch_id + source_trace_ids` 查询；writer 失败停止后续段并写 `companion_delivery_trace_incomplete`，而不是任取一个用户 Message trace；本期不新增 segment 明细表或发送 cursor。

若 `final_answer` 缺失或只有 `[[SEND]]` 而分段结果为零，则不是 usable final：不调用 normal segment completion，发送一条安全文案、保存该消息 ID，并写 `error_code=companion_final_empty`。

**已裁定的 companion restart 语义**：delivery 中 bot generation 退出时不自动重发或续发旧段，不引入 segment ledger/outbox。新 generation 的启动 reconciliation 只可选择 `status=processing AND delivery_stage=companion_delivery_started AND companion_delivery_batch_id IS NOT NULL` 的行，按原 batch ID 条件标记其全部 source Message 为 `failed`、`companion_delivery_abandoned` 并清除 stage，同时以原 batch/source traces 写可能部分可见的无正文交付 trace；它不得因此调用飞书 SendText，也不得误标 work 或尚未进入 delivery 的 companion processing。旧 CC Workspace Bot 的 `FINAL_REPLY.md`/Claude hook 也不迁移；若后续需要同等 persona 输出质量，另开 Codex-native output-policy Story。

### 6.3 飞书卡片模板

#### Work 模式

```json
{
  "schema": "2.0",
  "config": {"streaming_mode": true, "update_multi": true},
  "body": {"elements": [
    {"tag": "markdown", "element_id": "progress_text", "font_color": "grey", "content": "*等待 Codex 进展…*"},
    {"tag": "hr"},
    {"tag": "markdown", "element_id": "final_text", "content": "*思考中…*"}
  ]}
}
```

#### Companion 模式

```
纯文本发送，不带卡片
通过 [[SEND]] 分段模拟人类打字节奏
```

### 6.4 按钮回调处理

```go
func (w *Worker) handleCardAction(action string, msg *Message) {
    switch action {
    case "cancel":
        w.handleCancel(msg.Context)
    case "retry":
        // 重新发起上一轮
        w.handleRetry(msg.Context)
    case "approve":
        w.handleApprove(msg.Context)
    case "deny":
        w.handleDeny(msg.Context)
    }
}
```

---

## 7. 命令系统

### 7.1 支持的命令

| 命令 | 执行边界 | 可见结果 |
|------|----------|----------|
| `/new` | Worker 控制屏障：清等待 FIFO、取消 active work/delivery、archive 后 CAS clear Thread | 静态确认卡；下一条普通文本创建新 Thread |
| `/cancel`（`/stop` 兼容） | Worker 控制屏障：清等待 FIFO、取消 active work/delivery | 静态确认卡；Thread 保留 |
| `/status` | Router receipt 后直接并发调用 `account/rateLimits/read` 与 `account/usage/read` | 静态状态卡，显示查询时间、bucket 百分比/窗口/reset；部分失败安全降级 |
| `/goal <目标>` | Worker 独占 GoalRun：ensure Thread → `thread/goal/set(active)` → objective `turn/start`；连续 Turn 持续关联至 Goal terminal | 正常流式卡/companion 交付与最终 Goal 状态；不发送静态成功确认 |
| `/help` | Router receipt 后静态响应 | 固定命令帮助卡 |

所有命令先持久化并去重。命令 effect 和 reply outcome 分开记录；静态 card 失败只尝试一次等价 text fallback，unknown 不重发。命令卡不修改活动 S04 输出卡，活动输出只由原 Batch 的 Terminal 路径收尾。

### 7.2 隐私与命令解析

命令仅在 ASCII `/` 为首 rune 时识别，token 忽略大小写、外层 Unicode 空白；只有 `/goal` 接受非空、最多 4,000 Unicode rune 的 remainder。`/goal` 的 objective 不写 MySQL、日志、飞书、workflow 或 debug timeline；**S08 的个人自用 Langfuse Project 按操作者裁决记录 objective 与其他业务原文**。receipt 仅保存 `/goal [redacted]`、SHA-256 与字节长度。唯一 App Server reader 在 raw timeline 写入前使用预注册的 thread→Goal registry 消毒 `params/result.goal.objective` 与首个 Goal `userMessage.content[].text`，保留 digest/byte length；不能安全判定的 Goal raw envelope fail closed，不写 raw。其余 slash 命令不带参数。全角斜杠仍是普通文本。

---

## 8. Workspace 管理

### 8.1 目录结构（不变）

```
workspaces/<app-id>/
├── AGENTS.md              # 项目指令；可配置 fallback 加载既有 CLAUDE.md
├── .agents/skills/        # 项目技能
└── memory/                # 长期记忆（flock 保护）
```

### 8.2 初始化

```bash
./init_workspace.sh <app-id> <workspace-dir> <feishu-app-id> <feishu-app-secret>
```

脚本行为:
1. 创建目录结构
2. 复制模板文件
3. 通过 appctl 在 MySQL 写入 App 凭证与运行配置

### 8.3 Per-App 模型配置

模型与 effort 是 `apps` 表的 per-App 字段；`appctl` 是一期人工管理入口，YAML 不再保存第二份应用配置。

---

## 9. 数据层设计

### 9.1 MySQL Schema

#### apps 表

```sql
CREATE TABLE apps (
    id CHAR(36) PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    feishu_app_id VARCHAR(64) NOT NULL,
    feishu_app_secret VARCHAR(256) NOT NULL,
    workspace_dir VARCHAR(1024) NOT NULL,
    workspace_mode VARCHAR(16) NOT NULL DEFAULT 'work',
    model VARCHAR(128) NOT NULL,
    reasoning_effort VARCHAR(32) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE KEY uk_apps_name (name),
    UNIQUE KEY uk_apps_feishu_app_id (feishu_app_id)
);
```

#### chat_groups 表

```sql
CREATE TABLE chat_groups (
    id CHAR(36) PRIMARY KEY,
    app_id CHAR(36) NOT NULL,
    chat_type VARCHAR(16) NOT NULL,
    chat_id VARCHAR(128) NOT NULL,
    codex_thread_id VARCHAR(128) NULL,
    codex_tool_catalog_version VARCHAR(64) NOT NULL DEFAULT 's05-feishu-v2',
    catalog_upgrade_state ENUM('stable', 'archive_pending', 'start_pending') NOT NULL DEFAULT 'stable',
    catalog_upgrade_from_thread_id VARCHAR(128) NULL,
    catalog_upgrade_target VARCHAR(64) NULL,
    schedule_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    last_message_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    FOREIGN KEY (app_id) REFERENCES apps(id),
    UNIQUE KEY uk_chat_groups_app_chat (app_id, chat_type, chat_id)
);
```

#### messages 表

```sql
CREATE TABLE messages (
    id CHAR(36) PRIMARY KEY,
    trace_id CHAR(32) NOT NULL,
    chat_group_id CHAR(36) NOT NULL,
    feishu_event_id VARCHAR(128) NOT NULL,
    feishu_user_message_id VARCHAR(128) NOT NULL,
    feishu_bot_message_id VARCHAR(128) NULL,
    sender_open_id VARCHAR(128) NULL,
    user_content MEDIUMTEXT NOT NULL,
    assistant_content MEDIUMTEXT NULL,
    status VARCHAR(16) NOT NULL,
    error_code VARCHAR(64) NULL,
    companion_delivery_batch_id CHAR(36) NULL,
    delivery_stage VARCHAR(40) NULL,
    duration_ms BIGINT UNSIGNED NULL,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    completed_at DATETIME(3) NULL,
    FOREIGN KEY (chat_group_id) REFERENCES chat_groups(id),
    UNIQUE KEY uk_messages_trace_id (trace_id),
    UNIQUE KEY uk_messages_event (chat_group_id, feishu_event_id),
    UNIQUE KEY uk_messages_user_message (chat_group_id, feishu_user_message_id),
    KEY idx_messages_group_created (chat_group_id, created_at),
    KEY idx_messages_delivery_reconcile (status, delivery_stage, companion_delivery_batch_id)
);
```

S04 通过一条 forward-only migration 增加 `companion_delivery_batch_id` 与 `delivery_stage`，初始均为 `NULL`。它们不是 segment outbox：只在 companion 的第一条可能可见发送前写入 `companion_delivery_started`，用于同 Batch 条件收尾和重启时精确标记 abandoned；当前 generation 的 `CompleteBatch` 或 `FailCompanionDelivery` 成功后、或启动 abandoned 收尾后，必须清除 stage，batch ID 保留作为审计关联键。

#### attachments 表（S05）

附件下载后的 `relative_path` 是本机受控目录中 `session_id/attachment_id/<safe-original-name>` 的相对路径；`session_id` 和 `attachment_id` 都是 UUID，重复文件名由 attachment UUID 目录隔离。`relative_path` 使用 `VARCHAR(2048) CHARACTER SET utf8mb4`，以便最终安全叶名保留 Unicode；其 MySQL 唯一索引使用 700-character 前缀以满足 utf8mb4 的索引字节上限，受控路径中的 attachment UUID 位于此前缀内。下载先写入固定、有界的临时叶名，再原子 rename 到最终名；最终名按 UTF-8 字节数限制在文件系统单叶边界内，并尽量保留扩展名。清理兼容历史 `payload` 叶名，但只会删除 UUID 层级和受控叶名同时匹配的目录。

#### 文档 Owner 转移（S05.1）

`feishu.doc_create_and_announce` 创建 docx 后，必须只将该文档的 Owner 转给触发该 tool call 的可信 `sender_open_id`。该 sender identity 从 ingress 经 message/Worker 的 memory-only `ActorPrincipal` 传入 action route；group 的 `chat_id`、p2p/group 的 reply target、模型 tool arguments 都不得作为 Owner。调用 Drive `transfer_owner` 是 best-effort：文档 URL 仍是主交付，transfer rejected/unknown 只使公开 action result 返回 `owner_transferred:false` 和稳定 outcome，且绝不触发第二次文档创建或转移。document ID、open ID 和响应正文不得进入普通日志、workflow 或未加密 action result；S08 的个人自用 Langfuse Project 按裁决记录这些**业务**值。任何 token、header、Authorization/Cookie 与运行凭据仍不得进入任何外发观察面；同一 action call 的加密 replay 不重复副作用。

#### scheduled_tasks / scheduled_task_runs / scheduled_task_deliveries / scheduled_task_tool_calls（S06）

S06 的定时任务以 MySQL 为唯一真相源，而不是 workspace YAML、fsnotify 或调度库内存状态。`scheduled_tasks` 绑定 `app_id + chat_group_id + creator_open_id`，保存固定 `Asia/Shanghai` 的五字段 Cron、明文 Prompt 或直接本机 Script `command`、silent/enabled/version 和 UTC `next_run_at`。这是个人本地部署的明确产品选择：创建者、任务 payload、run payload 和 tool-call replay result 均可直接在 MySQL 检查；migration 006/007 由启动时一次性解密旧行、写入明文字段并清空旧密文字段。Prompt 工具参数的 Schema 必须要求 Agent 将当前用户需求改写为未来可独立执行的用户命令：未来 Agent 不带创建时对话上下文，必须保留动作、输入位置、筛选规则、交付物与交付渠道，将“发给我/给我”消歧为“发送到当前飞书会话”，且不得保留“你/我/这里/刚才”等上下文指代或附加未请求的步骤；调度器只原样传递该 payload，不自行拼接 Prompt。Script `command` 参数则保存用户要求的实际完整 shell 命令，不得自行添加步骤、参数或 `script_id` 登记要求。`scheduled_task_runs` 以 `UNIQUE(task_id, scheduled_for)`、claim token/queued/running lease 记录每个 slot 的不可变明文 payload、Prompt thread/turn 或 Script exit/output HMAC；未知中断绝不自动重放。`scheduled_task_deliveries` 为每次 result card 或 rejected 后 fallback text 建立独立 intent/outcome，unknown 永不重发。`scheduled_task_tool_calls` 以 App Server `app/thread/turn/call` identity 和明文 result 保持 `schedule.list_own/create/update` 的 at-most-once 回放。`schedule.update` 使用 `list_own` 返回的 `id`（而不是另名 `task_id`），可以可选携带同样返回的 `kind`；服务只接受与原任务相同的类型，绝不以它变更 `prompt|script`；任何 dynamic-tool Schema 改动均递增持久 catalog version，先 archive/start 新 Thread 再处理请求。历史 `scheduled_script_definitions` 表不再由 runtime 使用。S06 scheduler 只用 Cron parser 计算 `next_run_at`，由 `SELECT FOR UPDATE` MySQL transaction 领取 run；Prompt 以独立 ScheduledPrompt profile 进入既有同频道 Worker FIFO，Script 由 bot 当前 OS 用户在任务 App workspace 通过 `shell_path -c command` 直接执行；没有管理员登记、低权限或网络隔离。

### 9.2 内存队列

```go
// Worker 内部的普通消息 FIFO；Command 不进入此队列。
type MessageQueue struct {
    ch     chan *Message    // buffered channel, depth 64
    closed atomic.Bool
}

func (q *MessageQueue) Push(msg *Message) error {
    if q.closed.Load() {
        return ErrWorkerStopped
    }
    select {
    case q.ch <- msg:
        return nil
    default:
        return ErrQueueFull  // 队列满，返回确定性拒绝
    }
}
```

---

## 10. 配置体系

### 10.1 config.yaml

```yaml
# 应用配置（name、Feishu App ID/Secret、CWD、模式、模型、effort、enabled）
# 只存 MySQL apps 表，通过 appctl 管理；不在此文件重复定义。

# ===== Codex App Server 全局配置 =====
codex:
  binary: "codex"
  args: ["app-server", "--stdio"]
  rpc_timeout_seconds: 30
  turn_timeout_seconds: 3000
  stream_idle_timeout_seconds: 3000
  interrupt_grace_seconds: 10
  approval_policy: "never"
  sandbox: "danger-full-access"

# ===== Worker 池配置 =====
worker:
  max_workers: 20
  idle_timeout_minutes: 30
  queue_depth: 64
  in_process_timeout_minutes: 90
  stop_grace_seconds: 10

# ===== 流式配置 =====
streaming:
  card_flush_interval_ms: 250
  progress_max_bytes: 8192
  final_card_max_bytes: 18432
  companion_segment_delay_ms: 400 # 仅覆盖旧 SegmentOptions.BaseDelay；其余延迟常量固定
  card_request_timeout_seconds: 10

# ===== 定时任务 =====
schedule:
  tick_interval_ms: 1000
  misfire_grace_seconds: 60
  max_enabled_tasks_per_owner: 100
  max_enabled_tasks_per_app: 1000
  max_prompt_dispatch_per_tick: 20
  payload_keys:                         # active key first；key_env 不进版本库
    - version: 1
      key_env: "CODEX_WORKSPACE_BOT_SCHEDULE_PAYLOAD_KEY_V1"
  owner_hmac_keys:
    - version: 1
      key_env: "CODEX_WORKSPACE_BOT_SCHEDULE_OWNER_HMAC_KEY_V1"
scripts:
  enabled: false
  shell_path: "/bin/sh"
  timeout_seconds: 300
  max_output_bytes: 24576
  max_concurrent: 2

# ===== 数据层 =====
database:
  driver: "mysql"
  host: "127.0.0.1"
  port: 3306
  name: "codex_workspace_bot"
  user: "codex_workspace_bot"
  password_env: "CODEX_WORKSPACE_BOT_DB_PASSWORD"
  max_open_conns: 10
  max_idle_conns: 5

# ===== Langfuse（S08，个人自用自托管 Project） =====
observability:
  langfuse:
    enabled: false
    base_url: "https://langfuse.example.local"
    public_key_env: "LANGFUSE_PUBLIC_KEY"
    secret_key_env: "LANGFUSE_SECRET_KEY"
    project_id: ""
    environment: "development"
    export_timeout_seconds: 2
    max_queue_size: 4096

# ===== HTTP Server =====
server:
  port: 8080

# ===== 清理 =====
cleanup:
  attachments_retention_days: 7
  attachments_max_days: 30
  schedule: "0 2 * * *"
```

`streaming` 是配置 schema 的一部分：缺失 `companion_segment_delay_ms` 使用 400；显式值必须满足 `0 < value <= 2000`，否则启动失败。实现时须同步 `config.yaml.template`、配置解析和该启动校验的单元测试。

`scripts` 是个人本机的直接执行能力：当 `enabled=true` 时，启动校验只验证 `shell_path` 为绝对 regular executable，以及 timeout、输出上限和并发限制有效。Script command 以 bot 当前 OS 用户在任务 App workspace 通过 `shell_path -c command` 运行；不使用管理员登记、`script_id`、降权 runner、资源隔离或网络隔离。超时/取消仍杀死并回收整个进程组，避免遗留子进程。

### 10.2 环境变量（仅数据库密码等）

```bash
# 数据库密码（不写入 config.yaml）
export CODEX_WORKSPACE_BOT_DB_PASSWORD="xxx"

# Langfuse（如果不放 config.yaml）
export LANGFUSE_PUBLIC_KEY="pk-xxx"
export LANGFUSE_SECRET_KEY="sk-xxx"
```

---

## 11. 可观测性

### 11.1 Langfuse 集成（编排层直连）

**不用 hooks**，直接从 App Server Notification events 采集：

Langfuse 遵从 W3C Trace Context：trace ID 是 32 位小写十六进制，observation ID 是 16 位小写十六进制。S08 以 Go OpenTelemetry OTLP/HTTP protobuf 向 `${base_url}/api/public/otel/v1/traces` 写入，使用现有 realtime `messages.trace_id` 或 claimed scheduled run 的持久化 32-hex `trace_id` 作为一棵 canonical Trace 的 root ID。

这是操作者个人自用的**新自托管 Project**，按明确裁决写入明文业务数据：prompt、用户 input/output、可获得的 App Server reasoning Item、工具参数/结果、文档/附件正文、OpenID、chat/message/document/file ID、路径和 URL。一个 Batch/Turn 一棵 Trace，Session ID 为 `app_id:chat_type:chat_id`，user ID 为 `sender_open_id`；不再提供 metadata-only、HMAC、ID hash 或内容脱敏开关。唯一排除项是运行凭据：Langfuse/飞书 key、Authorization、Cookie、access/refresh token、数据库密码和进程环境 secret。适配器只从 App Server notification/server-request 采集，使用有界 fail-open exporter；导出失败不阻塞 Codex、Worker 或飞书。

`turn/completed.usage` 是 Turn 与会话总计的唯一权威来源，原始 input/output/cached/reasoning/total counters 明文保存；只有当包含关系与 total 算术成立时，才映射为互斥的 Langfuse `usage_details` buckets。`thread/tokenUsage/updated` 只以 `(generation,seq)` 去重的快照 delta 累积到可证明的 `agent.loop`，绝不混入 session total。`turn_usage_ledger` 的 `(trace_id,turn_id)` 首次插入才事务性增加 `session_usage_totals`，防止重连、重复 terminal 或 exporter retry 双算。

### 11.2 可观测维度

| 维度 | 数据来源 | 用途 |
|------|---------|------|
| Token 用量 | `thread/tokenUsage/updated` | 成本分析 |
| Turn 延迟 | `turn/started` → `turn/completed` 时差 | 性能监控 |
| 错误率 | `error` 通知 | 异常告警 |
| 审批统计 | 后续 approvals 审计记录 | 安全审计 |
| Worker 利用率 | 内存指标 | 容量规划 |
| Receiver 状态 | connecting / connected / reconnecting / fatal / stopped | 多 App 隔离与排障 |
| Companion 分段交付 | workflow JSONL：batch/source traces、segment index、SHA-256、result、message ID、retry | partial/unknown/abandoned 的无正文可追踪证据 |

### 11.3 结构化日志

```go
slog.Info("turn_completed",
    "app_id", app.ID,
    "channel_key", worker.channelKey,
    "thread_id", session.ThreadID,
    "turn_id", turnID,
    "model", app.Model,
    "input_tokens", usage.InputTokens,
    "output_tokens", usage.OutputTokens,
    "duration_ms", duration,
)
```

单 App Receiver 的错误不退出主进程：其状态变为 `reconnecting` 或 `fatal` 并记录 `app_id`，其他 App 继续工作。MySQL 不可用、配置错误和 App Server 进程级失败才是全局健康问题。该策略与旧 CC Workspace Bot 的独立 receiver goroutine 一致。

本机服务自行维护 `logging.dir`（默认 `logs`）中的 JSON Lines 日志：普通服务日志为 `server.log`，工作流日志为 `server.log.wf`。日志级别只允许 `debug`、`info`、`error`，默认 `info`。后台任务每分钟检测时间边界：跨小时将 current 文件切分为 `server-YYYYMMDDHH.log`（以及 WF 对应文件），跨日将上一日的已切分文件移动到 `logs/YYYYMMDD/`。启动时补偿遗漏切分/归档；不依赖外部 cron/logrotate，也不在一期删除归档。

S04 companion 每次 Feishu segment API result 后、下一段启动前，必须向 `server.log.wf` 追加 `event=companion_segment_delivery`：`batch_id,source_trace_ids,thread_id,turn_id,segment_index,text_sha256,result,reason|null,message_id|null,retry_count,at`。首次 429 用 `result=rejected,reason=rate_limited,retry_count=0` 记录，500ms 后同段重试为 `retry_count=1`；`reason` 仅可为稳定枚举。正文、marker、internal token、API 原文均不得记录；writer 失败即停止余段、形成 `companion_delivery_trace_incomplete`，不能静默继续。S08 Langfuse 对应个人 Project 可写明文业务 trace，但仍 fail-open，不能取代这条本机 workflow 证据。

每一个 outbound Feishu SDK 调用在完成后都必须以 Info 写入 `event=feishu_api_call`：`app_id,operation,outcome,code,subcode,log_id,duration_ms`。无论成功、平台拒绝、空响应还是 transport failure 都记录一条；`subcode` 仅从平台错误中解析数字，`log_id` 用于飞书侧排查。不得记录请求/响应正文、access token、原始 chat/message/document/file/image/open ID 或平台错误原文。

**S03 个人本地调试例外**：当 `logging.level=debug`，唯一 App Server reader 在分类前按收到顺序 `Write+Sync` 每条 server→client 原始 JSON-RPC event 到 `appserver-raw-<process-start>.ndjson`，并在 dispatch 前向 `appserver-event-<process-start>.jsonl` 写入同序号的结构化事件索引；dispatch 后向 `appserver-outcome-<process-start>.jsonl` 追加同 `seq` 的处理结果。Goal 事件的原始写入必须先经过 S07 的 registry sanitizer；`objective`、首个 Goal userMessage text 以及无法安全判定的 Goal envelope 不得落盘。event 至少区分时间/顺序、child generation、JSON-RPC 类别与 method/ID、App/Channel/ChatGroup/attempt、Thread/Turn/Item；outcome 记录最终路由与 dispatch 结果。按任一维度过滤、以 `seq` join 并排序可还原 event 时间线。任一 writer 失败即关闭该 generation，并将证据标记 incomplete，不能静默漏记 event。该例外不进入飞书、MySQL 或 Langfuse，测试完成后切回 `info`。不为此一次性 Story新增脱敏、留存或访问控制机制。

---

## 12. 功能清单

### 12.1 必须实现（P0）

| 功能 | 模块 | 说明 |
|------|------|------|
| 飞书 WS 接入 | feishu/ | 每个 enabled App 一个 receiver、状态可观测 |
| 消息解析 | feishu/ | text / p2p / group；topic_group 忽略 |
| ChatGroup 路由 | router/ | `(app_id, chat_type, chat_id)` 隔离 |
| Worker 池 | worker/ | group chat_id / p2p open_id 串行、max 20、队深 64、普通消息 merge、Idle/超时状态机 |
| Thread 管理 | worker/codexapp | ChatGroup 直接保存/清空 Codex Thread ID |
| Codex App Server 客户端 | codex/ | JSON-RPC + 事件流 |
| 流式卡片更新 | output/ | work 模式 |
| Companion 分段发送 | output/ | [[SEND]] + 打字延迟 |
| /new 命令 | command/ | 归档 + 新 Thread |
| /cancel 命令 | command/ | turn/interrupt |
| /status 命令 | command/ | account/rateLimits + usage |
| MySQL 持久化 | storage/ | apps/chat_groups/messages |
| Workspace 初始化 | workspace/ | 模板复制 + feishu.json |
| 配置加载 | config/ | Viper + 校验 |

### 12.2 重要功能（P1）

| 功能 | 模块 | 说明 |
|------|------|------|
| 欢迎事件 | feishu/ | Bot 入群 / 用户入群 |
| 定时任务（S06） | schedule/ + storage/ + worker/ | Agent `schedule` dynamic tools → MySQL task/run/claim → Cron parser；Prompt 进入原频道 Worker FIFO，Shell 受控执行，静默仅抑制自动交付 |
| 审批代理 | approval/ | 飞书审批卡片 |
| Langfuse 集成 | observability/ | Token + 延迟 + 错误 |
| 长期记忆 | workspace/ | flock 保护 |
| 附件输入与清理（S05） | attachment/ + storage/ | Worker 下载、retention CAS 清理与本地路径输入 |
| 飞书文档读取（S05） | feishuaction/ + feishu/ | 仅用当前 App 的 `Docx.Document.RawContent` 读取有效 docx URL，并把正文只交给当前 Codex Turn |
| 文档 Owner 转移（S05.1） | feishuaction/ + feishu/ | 创建的单篇 docx 转给当前消息发起人；失败仍公告 URL，群聊不以 chat ID 代替 owner |

### 12.3 未来功能（P2）

| 功能 | 说明 |
|------|------|
| 百炼模型接入 | 需要 Proxy 层 |
| 富文本、audio、media 与 OCR | S05 后续 Story 处理多资源解析、渲染与清理 |
| 推理过程展示 | item/reasoning/textDelta |
| 飞书卡片按钮 | 停止/重新生成/审批 |
| turn/steer | 运行中转向 |

---

## 附录 A: 关键流程时序图

### A.1 消息处理主流程

```
User    Feishu   Receiver   Router   Worker   CodexEngine   AppServer   MySQL   FeishuAPI
 │        │         │         │        │          │            │         │        │
 │─消息─→│         │         │        │          │            │         │        │
 │        │─WS────→│         │        │          │            │         │        │
 │        │         │─dispatch→│      │          │            │         │        │
 │        │         │         │─queue─→│         │            │         │        │
 │        │         │         │        │─getOrCreateSession──→│        │        │
 │        │         │         │        │←─session─────────────│        │        │
 │        │         │         │        │         │            │         │        │
 │        │         │         │        │─startTurn(cwd=ws_dir)│        │        │
 │        │         │         │        │         │─thread/start→│        │        │
 │        │         │         │        │         │←─thread/started│       │        │
 │        │         │         │        │         │─turn/start──→│        │        │
 │        │         │         │        │         │←─turn/started─│       │        │
 │        │         │         │        │         │            │         │        │
 │        │         │         │        │←─EventStream──────────│        │        │
 │        │         │         │        │         │←─item/completed(agentMessage)─│        │
 │        │         │         │        │         │            │         │        │
 │        │         │         │        │─Card.Update(card_json) / single-call PATCH fallback→│
 │←─卡片更新────────────────────────────────────────────────────────────────│
 │        │         │         │        │         │            │         │        │
 │        │         │         │        │         │←─completed─│        │        │
 │        │         │         │        │─record──────────────────→│        │        │
 │        │         │         │        │─Card.Update(final, streaming=false) / single-call PATCH fallback→│
 │←─最终卡片───────────────────────────────────────────────────────────────│
```

### A.2 /new 命令

```
User → /new
  │
  ▼
Worker.handleNew()
  ├─ 如果有活跃 turn → Engine.Interrupt()
  ├─ App Server: thread/archive（尽力而为）
  ├─ DB: chat_group.codex_thread_id = NULL
  └─ 回复: "已开启新会话"
  
下一条消息:
  Worker.process()
  ├─ 无 codex_thread_id → Engine.startThread()
  │   └─ JSON-RPC: thread/start → 新 Thread ID
  ├─ 新 Thread ID 回写 ChatGroup
  └─ 正常处理
```

### A.3 App Server 崩溃恢复

```
App Server 进程退出
  │
  ▼
CodexEngine 检测
  ├─ 标记所有 active turn → failed
  ├─ 重启 App Server 进程
  └─ 等待下一个 turn
  
下一个 turn:
  Worker.process()
  ├─ Engine.ensureThread(chatGroup.CodexThreadID)
  │   ├─ 尝试 thread/resume
  │   │   ├─ 成功 → 继续用旧 Thread
  │   │   └─ 失败 → thread/start 新建
  │   └─ 更新 ChatGroup.CodexThreadID
  └─ 正常处理
```

---

> **文档结束**
> v4.2：补齐 S04 第二轮 blocker：marker 写入后的 control/trace/local failure 必须由当前 generation 以 `FailCompanionDelivery` 事务收尾；仅最终无法收尾并退出的 marker 才由重启标记 abandoned。S03 的 raw + event + outcome 仍是完整本地观察层，不直接暴露给飞书。
