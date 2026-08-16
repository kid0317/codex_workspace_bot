# Codex App Server 协议调研

> **版本**: v1.0 | **日期**: 2026-07-10
> **Codex CLI**: 0.144.1 / 0.143.0（调研时验证版本）
> **目的**: 为 cc_workspace_bot → codex_workspace_bot 重写提供协议层设计依据

---

## 目录

1. [概述与定位](#1-概述与定位)
2. [传输层](#2-传输层)
3. [cwd 的完整生命周期](#3-cwd-的完整生命周期)
4. [初始化握手](#4-初始化握手)
5. [Thread 生命周期](#5-thread-生命周期)
6. [Turn 生命周期（核心）](#6-turn-生命周期核心)
7. [流式事件全表](#7-流式事件全表)
8. [审批流](#8-审批流)
9. [Turn 对象结构](#9-turn-对象结构)
10. [关键枚举与类型](#10-关键枚举与类型)
11. [实现要点与陷阱](#11-实现要点与陷阱)

---

## 1. 概述与定位

### 1.1 App Server 是什么

App Server 是 Codex CLI 的**可编程嵌入模式**——将完整的 Codex agent（模型调用 + 工具执行 + 审批 + 线程管理）暴露为 JSON-RPC 2.0 协议。外部程序作为客户端驱动 agent 运行。

### 1.2 与 Claude CLI 模式对比

| 维度 | Claude CLI（现有 cc_workspace_bot） | Codex App Server |
|------|------|------|
| 进程模型 | 每条消息 fork 一个 `claude` 进程 | 长生命周期进程，多 turn 复用 |
| 通信 | 命令行参数 + stream-json stdout | JSON-RPC 2.0 双向 |
| 上下文恢复 | `--resume session_id` | `thread/resume threadId` |
| 审批 | TUI 交互（headless 不适用） | 协议级审批请求/响应 |
| 中断 | kill 进程 | `turn/interrupt` 优雅中断 |
| 方向引导 | 不支持 | `turn/steer` 运行中转向 |
| 状态管理 | JSONL 文件（易污染） | 服务端管理 |

### 1.3 启动命令

```bash
# stdio 模式（重写首选）
codex app-server --stdio

# WebSocket 模式（未来扩展）
codex app-server --listen ws://127.0.0.1:8080 --ws-auth capability-token

# Daemon 模式（后台长驻）
codex app-server daemon start
```

### 1.4 协议规模

| 维度 | 数量 |
|------|------|
| Schema 定义 | **515 个** |
| Client→Server 请求 | **80+** 方法 |
| Server→Client 通知 | **70+** 方法 |
| Server→Client 请求（审批） | **11** 方法 |

---

## 2. 传输层

### 2.1 JSON-RPC 2.0 消息格式

所有消息都遵循 JSON-RPC 2.0 标准，stdio 模式下**每行一个 JSON 消息**（newline-delimited）。

Docker 发行模式为了把能执行 Shell 的 Agent 与 Bot/飞书/数据库 Secret 隔离到不同容器，可以在 stdio 两端之间加入一个**不解析、不改写、不缓存**的 byte-transparent TCP bridge。Bot 侧 `codex-remote` 仍表现为 stdio child，Codex 容器内 `codex-bridge` 独占启动唯一的 `codex app-server --stdio`；每行 JSON 的顺序、边界、backpressure、EOF 和进程生命周期必须原样传播。它不是共享 App Server daemon，也不能接受第二个并发 client。是否可用仍以 Bot 完成 JSON-RPC `initialize` 为准，而不是只看 TCP 端口存活。

#### 四种消息类型

```jsonc
// 1. Client → Server Request（期望响应）
{"jsonrpc":"2.0", "id":"req-1", "method":"turn/start", "params":{...}}

// 2. Server → Client Response（请求的响应）
{"jsonrpc":"2.0", "id":"req-1", "result":{...}}

// 3. Server → Client Notification（单向推送，无 id，无需响应）
{"jsonrpc":"2.0", "method":"item/agentMessage/delta", "params":{...}}

// 4. Server → Client Request（审批请求，期望客户端响应）
{"jsonrpc":"2.0", "id":"approval-1", "method":"item/commandExecution/requestApproval", "params":{...}}

// 5. Client → Server Response（审批响应）
{"jsonrpc":"2.0", "id":"approval-1", "result":{"decision":"allow"}}

// 6. Error
{"jsonrpc":"2.0", "id":"req-1", "error":{"code":-32600, "message":"..."}}
```

### 2.2 Request ID

- 类型: `string | integer (int64)`
- Client 发起的请求必须有唯一 id
- Server 的审批请求也有 id，Client 必须在响应中使用相同 id
- Notification 没有 id

### 2.3 可选 W3C Trace Context

Request 可携带 `trace` 字段用于分布式追踪：
```json
{
  "jsonrpc": "2.0",
  "id": "req-1",
  "method": "turn/start",
  "params": {...},
  "trace": {
    "traceparent": "00-xxx-xxx-01",
    "tracestate": "key=value"
  }
}
```

---

## 3. cwd 的完整生命周期

**这是重写方案 B（单进程多 workspace）的核心机制。**

### 3.1 三级 cwd 层次

```
Level 1: App Server 进程启动 cwd
  ↓  可通过 thread/start 覆盖
Level 2: Thread 级 cwd（thread 创建时确定）
  ↓  可通过 turn/start 覆盖
Level 3: Turn 级 cwd（单次 turn 的工作目录）
  ↓  "for this turn and subsequent turns"
```

### 3.2 各阶段详细说明

#### Level 1: App Server 启动 cwd

```bash
# 启动时通过 shell cwd 指定
cd /some/dir && codex app-server --stdio

# 或通过 cmd.Dir 设置（Go 客户端）
cmd := exec.Command("codex", "app-server", "--stdio")
cmd.Dir = "/some/dir"
```

**影响**:
- 作为所有 thread 的默认 cwd（如果 thread/start 不指定）
- 影响 CODEX_HOME 的相对路径解析
- **不影响** AGENTS.md 加载（AGENTS.md 跟着 thread/turn 的 cwd）

#### Level 2: Thread 级 cwd

```json
// thread/start 请求
{
  "method": "thread/start",
  "params": {
    "cwd": "/workspace/investment-assistant",  // ← 指定 thread 的工作目录
    "approvalPolicy": "never",
    "model": "codex-mini"
  }
}

// thread/start 响应
{
  "result": {
    "thread": {"id": "thread-xxx", ...},
    "cwd": "/workspace/investment-assistant",  // ← 确认 thread 的 cwd
    "model": "codex-mini",
    ...
  }
}
```

**影响**:
- 该 thread 下所有 turn 的默认 cwd
- **AGENTS.md 加载**: `CODEX_HOME/AGENTS.md`（全局层）+ `{cwd}/AGENTS.md`（项目层）
- **skills 加载**: `{cwd}/.claude/skills/` 下的技能
- **environment_context** 中的 `<cwd>` 标签

#### Level 3: Turn 级 cwd（动态覆盖）

```json
// turn/start 请求
{
  "method": "turn/start",
  "params": {
    "threadId": "thread-xxx",
    "cwd": "/workspace/health-assistant",  // ← 覆盖！"for this turn and subsequent turns"
    "input": [{"type": "text", "text": "今天吃了什么"}]
  }
}
```

**关键语义**: "Override the working directory for this turn and subsequent turns."
- 当前 turn 在指定 cwd 下执行
- **后续 turn** 也继承这个 cwd（除非再次覆盖）
- 这意味着可以在同一个 thread 内动态切换 workspace！

### 3.3 cwd 与 AGENTS.md 加载

从 `codex debug prompt-input` 验证的加载格式：

```
发送给模型的系统消息:

<INSTRUCTIONS>
# Global Layer
[CODEX_HOME/AGENTS.md 内容]

--- project-doc ---

# Workspace Layer
[{cwd}/AGENTS.md 内容]
</INSTRUCTIONS>

<environment_context>
  <cwd>{当前 cwd}</cwd>
  <shell>bash</shell>
  <current_date>2026-07-10</current_date>
  <timezone>Asia/Shanghai</timezone>
  <filesystem>
    <workspace_roots>
      <root>{当前 cwd}</root>
    </workspace_roots>
    ...
  </filesystem>
</environment_context>
```

**关键结论**:
1. **CODEX_HOME/AGENTS.md**: 全局层，所有 workspace 共享（如 bot 的安全约束、通用指令）
2. **{cwd}/AGENTS.md**: 项目层，跟着 cwd 动态变化（每个 workspace 的角色定义、技能配置）
3. **`<cwd>` 在 environment_context 中动态注入**: 模型知道当前在哪个目录工作
4. **workspace_roots**: 也跟着 cwd 变化

### 3.4 实验验证

**实验**: 单 App Server 进程启动在 ws1，创建两个不同 cwd 的 thread：

```
App Server 启动 cwd: /tmp/codex_test/ws1

Thread 1: thread/start {cwd: "/tmp/codex_test/ws1"} → ✅ 成功
Thread 2: thread/start {cwd: "/tmp/codex_test/ws2"} → ✅ 成功（不同于启动 cwd！）
```

**结论**: 单 App Server 完全可以通过 thread/start 的 cwd 参数服务多个不同的 workspace。

### 3.5 重写方案 B 的 cwd 策略

```
Bot Orchestrator 收到飞书消息
  │
  ├─ 找到对应的 workspace_dir（由 channel_key → app_id → workspace_dir 映射）
  │
  └─ Engine.SendTurn({
       threadId: session.engine_thread_id,
       cwd: workspace_dir,           // ← 每次 turn 动态指定 workspace
       input: [{type: "text", text: prompt}],
     })
```

**好处**: 27 个 workspace 共享 1 个 App Server 进程，通过 cwd 动态路由。

---

## 4. 初始化握手

### 4.1 Client → Server: initialize

```json
{
  "method": "initialize",
  "params": {
    "clientInfo": {
      "name": "codex-workspace-bot",
      "version": "1.0.0",
      "title": "Codex Workspace Bot"
    },
    "capabilities": {
      "experimentalApi": true,
      "mcpServerOpenaiFormElicitation": false,
      "requestAttestation": false,
      "optOutNotificationMethods": ["thread/started"]
    }
  }
}
```

#### InitializeCapabilities 详解

| 字段 | 类型 | 说明 |
|------|------|------|
| experimentalApi | bool | 启用实验性 API 方法和字段 |
| mcpServerOpenaiFormElicitation | bool | 允许 MCP 服务器请求表单输入 |
| requestAttestation | bool | 启用 attestation/generate 请求 |
| optOutNotificationMethods | string[] \| null | 静默指定通知方法名（如 "thread/started"） |

### 4.2 Server → Client: Response

```json
{
  "result": {
    "userAgent": "codex/0.144.1 (Ubuntu 24.4.0; x86_64) ...",
    "codexHome": "/root/.codex",
    "platformFamily": "unix",
    "platformOs": "linux"
  }
}
```

**注意**: initialize 必须在所有其他请求之前完成。

---

## 5. Thread 生命周期

### 5.1 Thread 是什么

Thread = 一段完整的对话上下文。类似 Claude 的 session，但由 App Server 服务端管理状态。

### 5.2 核心方法

| 方法 | 说明 | 关键参数 |
|------|------|---------|
| `thread/start` | 创建新 thread | cwd, model, approvalPolicy, baseInstructions |
| `thread/resume` | 恢复已有 thread | threadId + 同 start 参数 |
| `thread/fork` | 分叉 thread | 从某个 turn 分叉 |
| `thread/archive` | 归档 thread | threadId |
| `thread/unarchive` | 取消归档 | threadId |
| `thread/delete` | 删除 thread | threadId |
| `thread/list` | 列表查询 | 过滤/分页参数 |
| `thread/read` | 读取内容 | threadId |
| `thread/compact/start` | 压缩上下文 | threadId |
| `thread/inject_items` | 注入 items（不启动 turn） | threadId, items |

### 5.3 Thread 元数据管理

| 方法 | 说明 |
|------|------|
| `thread/name/set` | 设置线程名称 |
| `thread/goal/set` | 设置线程目标 |
| `thread/goal/get` | 获取线程目标 |
| `thread/goal/clear` | 清除线程目标 |
| `thread/metadata/update` | 更新元数据 |

### 5.3.1 Goal 与首个 Turn（2026-07-13 官方语义校正）

`thread/goal/set`、`thread/goal/get` 与 `thread/goal/clear` 管理的是 Codex TUI `/goal` 所呈现的同一份持久 Thread Goal；它们本身不替代 `turn/start`。官方 long-running work 定义 `/goal` 的目标文本同时是**首个 prompt**与**完成条件**，因此嵌入式客户端实现“设置并立即开始”时必须：

1. 确保 Thread 已 start/resume；
2. `thread/goal/set {threadId, objective, status:"active", tokenBudget?}`；
3. 以同一 objective 调用 `turn/start`；
4. 在 `goal/set` 前注册 thread 级 Goal owner；将不带单一 turnId 的 `thread/goal/updated` 与该 Thread 的连续 `turn/started` / `turn/completed` 共同关联，直到 Goal 的 complete、paused 或 budget-limited 终态。

Goal continuation 是 App Server 在安全边界触发的事件驱动机制，客户端不得用无限循环伪造；但也不得把首个 `turn/completed` 误判为 Goal 完成。客户端必须为乱序的首 `turn/start` response、`turn/started`、`turn/completed` 与 terminal goal update 建立 generation/terminal fence，且取消必须先暂停 Goal、再 interrupt/drain Turn。目标最大 4,000 Unicode code point；新 objective 会替换旧目标并重置 Goal usage accounting。

### 5.4 thread/start 完整参数

```json
{
  "method": "thread/start",
  "params": {
    // 工作目录
    "cwd": "/workspace/investment-assistant",

    // 模型配置
    "model": "codex-mini",
    "modelProvider": "openai",
    "serviceTier": "default",

    // 审批策略
    "approvalPolicy": "never",
    "approvalsReviewer": "auto_review",

    // 沙盒
    "sandbox": "danger-full-access",

    // 指令注入
    "baseInstructions": "你是一个投资顾问...",
    "developerInstructions": "系统级指令...",

    // 个性
    "personality": "pragmatic",

    // 其他
    "ephemeral": false,
    "sessionStartSource": "startup",
    "threadSource": "feishu_message",
    "config": {}
  }
}
```

### 5.5 thread/start 响应

```json
{
  "result": {
    "thread": {
      "id": "019f4b81-b817-78e3-9dfc-407fc2d4c4f9",
      "status": "active",
      "name": null
    },
    "cwd": "/workspace/investment-assistant",
    "model": "codex-mini",
    "modelProvider": "openai",
    "approvalPolicy": "never",
    "approvalsReviewer": "auto_review",
    "sandbox": "danger-full-access",
    "instructionSources": ["/root/.codex/AGENTS.md", "/workspace/investment-assistant/AGENTS.md"]
  }
}
```

### 5.6 thread/resume

```json
{
  "method": "thread/resume",
  "params": {
    "threadId": "019f4b81-...",
    "cwd": "/workspace/investment-assistant"
  }
}
```

**三种恢复方式**（按优先级）:
1. By threadId: 从磁盘加载并恢复
2. By history: 从内存实例化并恢复
3. By path: 从指定路径加载并恢复

**注意**: 如果 threadId 对应一个**正在运行**的 thread，app-server 会重新加入该 thread。

---

## 6. Turn 生命周期（核心）

### 6.1 Turn 是什么

Turn = 一次完整的模型推理过程：用户输入 → 模型思考 → 工具调用 → 最终回复。

**一个 Thread 可以包含多个 Turn，形成多轮对话。**

### 6.2 Turn 的三种操作

| 方法 | 说明 | 时机 |
|------|------|------|
| `turn/start` | 启动新 turn | 用户发送消息时 |
| `turn/steer` | 运行中转向 | 用户想改变方向时 |
| `turn/interrupt` | 中断 turn | 用户想停止时（/stop） |

### 6.3 turn/start 完整参数

```json
{
  "method": "turn/start",
  "params": {
    // 必填
    "threadId": "019f4b81-...",
    "input": [
      {"type": "text", "text": "帮我分析一下持仓情况"}
    ],

    // 可选覆盖（"for this turn and subsequent turns"）
    "cwd": "/workspace/investment-assistant",
    "model": "codex-mini",
    "effort": "high",
    "summary": "auto",
    "approvalPolicy": "never",
    "personality": "pragmatic",

    // 输出约束
    "outputSchema": {
      "type": "object",
      "properties": {
        "analysis": {"type": "string"},
        "action": {"type": "string", "enum": ["buy", "sell", "hold"]}
      }
    },

    // 客户端追踪
    "clientUserMessageId": "feishu_msg_xxx",

    // 附加上下文
    "additionalContext": {
      "source_id_1": {"key": "value"}
    }
  }
}
```

#### UserInput 类型

```json
// 文本输入
{"type": "text", "text": "用户的消息"}

// 图片输入（如果有）
{"type": "image", "url": "file:///path/to/image.png"}

// 文件输入（如果有）
{"type": "file", "path": "/path/to/file.pdf"}
```

### 6.4 turn/steer（运行中转向）

```json
{
  "method": "turn/steer",
  "params": {
    "threadId": "019f4b81-...",
    "expectedTurnId": "turn-xxx",  // 确保转向的是正确的 turn
    "input": [
      {"type": "text", "text": "等等，先不要分析技术面，看看资金流向"}
    ]
  }
}
```

**语义**: 在 turn 还在执行时，给模型新的方向指引。模型会在当前 turn 内响应这个指引。

### 6.5 turn/interrupt（中断）

```json
{
  "method": "turn/interrupt",
  "params": {
    "threadId": "019f4b81-...",
    "turnId": "turn-xxx"
  }
}
```

**语义**: 优雅中断当前 turn。模型会停止推理，已有的输出会保留。

**使用场景**:
- 用户发送 `/stop`
- turn 超时
- 审批被拒绝后需要停止

### 6.6 Turn 状态机

```
              turn/start
                 │
                 ▼
          ┌─────────────┐
          │  inProgress  │◄──────┐
          └──────┬──────┘       │
                 │              │
     ┌───────────┼──────────┐   │
     │           │          │   │
     ▼           ▼          ▼   │
  completed   interrupted  failed
                                   │
              turn/steer ──────────┘
              (在 inProgress 状态可 steer)
```

### 6.7 Turn 的完整事件流

一个 turn 从启动到完成，典型的 Server→Client 通知序列：

```
1. turn/started
   {threadId, turn: {id: "turn-xxx", status: "inProgress"}}

2. item/started
   {threadId, turnId, item: {type: "agentMessage", id: "item-1"}}

3. item/agentMessage/delta × N（文本流式输出）
   {threadId, turnId, itemId: "item-1", delta: "让我"}
   {threadId, turnId, itemId: "item-1", delta: "来分析"}
   {threadId, turnId, itemId: "item-1", delta: "一下..."}

4. item/started（工具调用）
   {threadId, turnId, item: {type: "commandExecution", id: "item-2"}}

5. item/commandExecution/outputDelta × N（命令输出）
   {threadId, turnId, itemId: "item-2", delta: "$ python3 analyze.py\n..."}

6. item/commandExecution/requestApproval（审批请求，Server→Client Request）
   {id: "approval-1", method: "item/commandExecution/requestApproval", params: {...}}

7. Client→Server Response
   {id: "approval-1", result: {decision: "allow"}}

8. item/commandExecution/outputDelta × N（命令继续输出）
   ...

9. item/completed（工具完成）
   {threadId, turnId, item: {type: "commandExecution", id: "item-2"}}

10. item/started（继续 agent 消息）
    {threadId, turnId, item: {type: "agentMessage", id: "item-3"}}

11. item/agentMessage/delta × N（继续文本输出）
    ...

12. item/completed
    {threadId, turnId, item: {type: "agentMessage", id: "item-3"}}

13. turn/completed
    {threadId, turn: {id: "turn-xxx", status: "completed", usage: {...}}}
```

---

## 7. 流式事件全表

### 7.1 Turn 生命周期事件

| 事件 | 语义 | 关键字段 |
|------|------|---------|
| `turn/started` | Turn 开始执行 | threadId, turn.id, turn.status="inProgress" |
| `turn/completed` | Turn 正常完成 | threadId, turn.id, turn.status="completed", turn.usage |
| `error` | 非致命错误 | threadId, turnId, error.message, willRetry |
| `turn/diff/updated` | Turn 产生的 diff 更新 | threadId, turnId, diff |
| `turn/plan/updated` | Turn 的计划更新 | threadId, turnId, plan |
| `turn/moderationMetadata` | 审核元数据 | threadId, turnId |

### 7.2 Item 生命周期事件

| 事件 | 语义 | 关键字段 |
|------|------|---------|
| `item/started` | Item 开始 | threadId, turnId, item.{type, id} |
| `item/completed` | Item 完成 | threadId, turnId, item |

### 7.3 Item 类型与对应的流式事件

#### 7.3.1 agentMessage（模型文本输出）

| 事件 | 语义 |
|------|------|
| `item/agentMessage/delta` | 文本增量（核心流式事件） |

```json
{
  "method": "item/agentMessage/delta",
  "params": {
    "threadId": "thread-xxx",
    "turnId": "turn-xxx",
    "itemId": "item-1",
    "delta": "这是一段增量文本"
  }
}
```

**重写要点**: 这是飞书卡片流式更新的核心数据源。每收到一个 delta，PATCH 飞书卡片追加文本。

#### 7.3.2 commandExecution（命令执行）

| 事件 | 语义 |
|------|------|
| `item/commandExecution/outputDelta` | 命令 stdout/stderr 增量 |
| `item/commandExecution/terminalInteraction` | 终端交互事件 |
| `item/commandExecution/requestApproval` | 命令执行审批（Server→Client Request） |

#### 7.3.3 fileChange（文件变更）

| 事件 | 语义 |
|------|------|
| `item/fileChange/outputDelta` | 文件变更输出（旧版） |
| `item/fileChange/patchUpdated` | 文件补丁更新（新版） |
| `item/fileChange/requestApproval` | 文件变更审批 |

#### 7.3.4 reasoning（推理过程）

| 事件 | 语义 |
|------|------|
| `item/reasoning/textDelta` | 推理文本增量 |
| `item/reasoning/summaryTextDelta` | 推理摘要增量 |
| `item/reasoning/summaryPartAdded` | 推理摘要新增部分 |

#### 7.3.5 autoApprovalReview（自动审批审查）

| 事件 | 语义 |
|------|------|
| `item/autoApprovalReview/started` | 自动审查开始 |
| `item/autoApprovalReview/completed` | 自动审查完成 |

#### 7.3.6 guardianApprovalReview（Guardian 审批审查）

| 事件 | 语义 |
|------|------|
| `item/guardianApprovalReview/started` | Guardian 审查开始 |
| `item/guardianApprovalReview/completed` | Guardian 审查完成 |

#### 7.3.7 mcpToolCall（MCP 工具调用）

| 事件 | 语义 |
|------|------|
| `item/mcpToolCall/progress` | MCP 工具调用进度 |

#### 7.3.8 plan（计划流式）

| 事件 | 语义 |
|------|------|
| `item/plan/delta` | 计划增量（experimental） |

### 7.4 Thread 状态事件

| 事件 | 语义 |
|------|------|
| `thread/started` | Thread 创建完成 |
| `thread/status/changed` | 状态变化 |
| `thread/archived` | 已归档 |
| `thread/unarchived` | 取消归档 |
| `thread/deleted` | 已删除 |
| `thread/closed` | 已关闭 |
| `thread/name/updated` | 名称更新 |
| `thread/goal/updated` | 目标更新 |
| `thread/goal/cleared` | 目标清除 |
| `thread/settings/updated` | 设置更新 |
| `thread/tokenUsage/updated` | Token 用量更新 |
| `thread/compacted` | 上下文已压缩 |

### 7.5 Hook 事件

| 事件 | 语义 |
|------|------|
| `hook/started` | Hook 开始执行 |
| `hook/completed` | Hook 执行完成 |

### 7.6 进程事件

| 事件 | 语义 |
|------|------|
| `process/outputDelta` | 进程输出增量 |
| `process/exited` | 进程退出 |
| `command/exec/outputDelta` | exec 命令输出增量 |

### 7.7 模型事件

| 事件 | 语义 |
|------|------|
| `model/rerouted` | 模型被重新路由 |
| `model/verification` | 模型验证 |
| `model/safetyBuffering/updated` | 安全缓冲更新 |

### 7.8 账户事件

| 事件 | 语义 |
|------|------|
| `account/updated` | 账户信息更新 |
| `account/rateLimits/updated` | 速率限制更新 |
| `account/login/completed` | 登录完成 |

### 7.9 系统事件

| 事件 | 语义 |
|------|------|
| `warning` | 警告通知 |
| `guardianWarning` | Guardian 安全警告 |
| `deprecationNotice` | 废弃通知 |
| `configWarning` | 配置警告 |
| `skills/changed` | 技能列表变化 |
| `fs/changed` | 文件系统变化 |
| `serverRequest/resolved` | Server 请求已解决 |

### 7.10 实时语音事件（Thread Realtime）

| 事件 | 语义 |
|------|------|
| `thread/realtime/started` | 实时会话开始 |
| `thread/realtime/itemAdded` | 实时 item 添加 |
| `thread/realtime/transcript/delta` | 转录增量 |
| `thread/realtime/transcript/done` | 转录完成 |
| `thread/realtime/outputAudio/delta` | 音频输出增量 |
| `thread/realtime/sdp` | WebRTC SDP 信令 |
| `thread/realtime/error` | 错误 |
| `thread/realtime/closed` | 关闭 |

---

## 8. 审批流

### 8.1 审批架构概览

```
App Server 检测到需要审批的操作
  │
  ▼
Server→Client Request（审批请求）
  {id: "approval-1", method: "item/xxx/requestApproval", params: {...}}
  │
  ▼
Bot 的 Approval Broker 处理
  │
  ├─ 自动决策（根据 guardrail 策略）
  │   ├─ allow → Client→Server Response {decision: "allow"}
  │   └─ deny  → Client→Server Response {decision: "deny"}
  │
  └─ 人工决策（转发给飞书用户）
      └─ 等待用户点击卡片按钮 → 响应
```

### 8.2 审批请求类型（11 种）

| 方法 | 说明 | 响应格式 |
|------|------|---------|
| `item/commandExecution/requestApproval` | 命令执行审批 | `{decision: "allow"/"deny"}` |
| `item/fileChange/requestApproval` | 文件变更审批 | `{decision: "allow"/"deny"}` |
| `item/permissions/requestApproval` | 权限审批 | `{permissions: {fileSystem, network}, scope}` |
| `item/tool/requestUserInput` | 工具请求用户输入 | 工具输入响应 |
| `mcpServer/elicitation/request` | MCP 表单输入 | 表单响应 |
| `item/tool/call` | 动态工具调用 | 工具结果 |
| `account/chatgptAuthTokens/refresh` | 刷新认证令牌 | 令牌响应 |
| `attestation/generate` | 生成证明 | 证明结果 |
| `currentTime/read` | 读取客户端时钟 | 时间响应 |
| `applyPatchApproval` *(DEPRECATED)* | 旧版补丁审批 | `{decision}` |
| `execCommandApproval` *(DEPRECATED)* | 旧版命令审批 | `{decision}` |

对于 namespace dynamic tool，App Server 的 `item/tool/call.tool` 是 namespace-local 名（例如 `create`），而不是展示给 Agent 的完整产品名 `schedule.create`。服务端必须仅在 exact attempt 的已注册 namespace 中把 local name canonicalize 为完整名；不得将 bare name 作为独立的公开产品工具或路由到其他 namespace。

S06 的 `schedule` tool 返回值及未来执行 Prompt 的语义由本地 MySQL task/run 真相源承载；在当前个人本地部署中，这些 task/run payload 选择明文保存，供运行排障直接检查。该存储决定不改变 App Server JSON-RPC 协议，也不放宽飞书凭据、token、原始消息正文和日志脱敏边界。

### 8.3 审批策略（AskForApproval）

```
"untrusted"   — 严格策略
"on-request"  — 模型主动请求时审批
"never"       — 永不审批（最宽松，danger-full-access 常用）
{granular: {...}} — 细粒度配置
```

### 8.4 审批者（ApprovalsReviewer）

```
"user"               — 用户审批（转发到飞书）
"auto_review"        — 自动审批
"guardian_subagent"  — Guardian 子代理审批
```

### 8.5 Personal-Trusted 场景的推荐配置

```json
{
  "approvalPolicy": "never",
  "approvalsReviewer": "auto_review",
  "sandbox": "danger-full-access"
}
```

配合 Bot 侧的 guardrail 策略:
- workspace 内读写 → auto allow
- 读敏感文件（.env, feishu.json, .ssh）→ deny
- workspace 外操作 → deny
- 网络访问 → 按白名单

---

## 9. Turn 对象结构

### 9.1 Turn 对象

```json
{
  "id": "turn-xxx",
  "status": "completed",
  "items": [
    {"type": "agentMessage", "id": "item-1", ...},
    {"type": "commandExecution", "id": "item-2", ...},
    {"type": "agentMessage", "id": "item-3", ...}
  ],
  "itemsView": "full",
  "startedAt": 1720612800,
  "completedAt": 1720612830,
  "durationMs": 30000,
  "usage": {
    "inputTokens": 1500,
    "outputTokens": 800,
    "cachedInputTokens": 500,
    "reasoningOutputTokens": 200,
    "totalTokens": 3000
  },
  "error": null
}
```

### 9.2 Turn 状态

```
"inProgress"  — 正在执行
"completed"   — 正常完成
"interrupted" — 被中断
"failed"      — 失败
```

### 9.3 TurnError

```json
{
  "message": "turn exceeded maximum duration",
  "additionalDetails": "Turn ran for 120s without completing",
  "codexErrorInfo": {
    "code": "TURN_TIMEOUT",
    "retryable": false
  }
}
```

### 9.4 Token Usage

```json
{
  "inputTokens": 1500,
  "outputTokens": 800,
  "cachedInputTokens": 500,
  "reasoningOutputTokens": 200,
  "totalTokens": 3000
}
```

**注意**: `usage` 字段在实际 wire format 中存在，但 JSON Schema 的 Turn 类型定义中**未正式声明**。需要从 `turn/completed` 通知的 params 中读取。

---

## 10. 关键枚举与类型

### 10.1 AskForApproval

```
"untrusted" | "on-request" | "never" | {granular: GranularApproval}
```

### 10.2 ApprovalsReviewer

```
"user" | "auto_review" | "guardian_subagent"
```

### 10.3 SandboxMode

```
"read-only"          — 只读沙盒
"workspace-write"    — 允许写 workspace
"danger-full-access" — 完全访问（无沙盒）
```

### 10.4 Personality

```
"none"        — 无个性
"friendly"    — 友好
"pragmatic"   — 务实
```

### 10.5 ReasoningEffort

```
"low" | "medium" | "high"
```

### 10.6 ReasoningSummary

```
"auto" | "concise" | "detailed" | "none"
```

### 10.7 Thread 状态

```
"active" | "archived" | "closed" | "deleted"
```

### 10.8 Item 类型

```
"agentMessage"             — 模型文本输出
"commandExecution"         — 命令执行
"fileChange"               — 文件变更
"reasoning"                — 推理过程
"autoApprovalReview"       — 自动审批审查
"guardianApprovalReview"   — Guardian 审批审查
"mcpToolCall"              — MCP 工具调用
"plan"                     — 计划
```

---

## 11. 实现要点与陷阱

### 11.1 客户端必须处理的模式

#### 模式 1: 请求-响应

```
Client: {"id":"1", "method":"turn/start", ...}
Server: {"id":"1", "result":{...}}     // 等待这个响应
Server: {"method":"turn/started",...}  // 然后开始通知流
Server: {"method":"item/agentMessage/delta",...}
Server: {"method":"turn/completed",...}
```

#### 模式 2: Server 发起请求（审批）

```
Server: {"id":"a1", "method":"item/commandExecution/requestApproval", ...}
Client: // 处理审批决策
Client: {"id":"a1", "result":{"decision":"allow"}}
Server: // 继续执行
```

**关键**: Client 必须能同时处理：
- 等待自己的 request 响应（通过 id 匹配）
- 处理 Server 主动发起的 request（审批）
- 处理 Server 推送的 notification（无需响应）

#### 模式 3: 并发处理

同一 thread 在同一时间只有一个 active turn。不同 thread 的 turn 也建议串行（serialized-v1）。

### 11.2 常见陷阱

| 陷阱 | 说明 | 规避 |
|------|------|------|
| **Notification 没有 id** | 不要尝试给 notification 发 response | 检查消息是否有 `id` 字段 |
| **审批请求的 id 是字符串** | 不要假设 id 是整数 | 用 string \| int64 解析 |
| **usage 不在 schema** | Turn schema 没有 usage 字段 | 从 `turn/completed` 实际 payload 读取 |
| **cwd 覆盖语义** | turn/start cwd "for this turn and subsequent turns" | 后续 turn 继承，除非再覆盖 |
| **deprecated 方法** | applyPatchApproval/execCommandApproval 已废弃 | 优先用新版 item/xxx/requestApproval |
| **experimentalApi** | 很多方法需要 capabilities.experimentalApi=true | initialize 时开启 |
| **stderr 丢弃** | App Server stderr 无法可靠捕获 | 通过 `warning` 通知获取 |

### 11.3 超时配置建议

| 场景 | 超时 | 说明 |
|------|------|------|
| JSON-RPC 请求响应 | 30s | initialize / thread/start 等 |
| Turn 执行 | 50min | 当前 Bot 的总 Turn 与无进展上限 |
| 审批等待 | 5min | 超时 → auto deny → interrupt turn |
| Turn 空闲 | 50min | 无事件 50 分钟 → interrupt |
| 连接心跳 | 5min | 长时间无消息，发 ping 检测 |

### 11.4 重连策略

App Server 进程崩溃时：
1. 检测到进程退出
2. 标记所有 inProgress turn 为 failed
3. 重新启动 App Server 进程
4. 对下一个 turn，尝试 `thread/resume`
5. 如果 resume 失败，`thread/start` 新建 thread

### 11.5 附件本地路径边界

附件下载完成后才把本机路径传入 `turn/start` input；最终叶名使用受控的原始文件名，临时文件使用不依赖原始名长度的固定有界名称并原子 rename。持久化的 `attachments.relative_path` 必须使用 `utf8mb4`，以保留 Unicode 文件名；清理只接受 session/attachment UUID 层级中的 legacy `payload` 或该受控最终叶名。

---

## 附录 A: 协议 Schema 位置

- 生成命令: `codex app-server generate-json-schema --out ./schema --experimental`
- V1 Schema: `codex_app_server_protocol.schemas.json` (82 definitions)
- V2 Schema: `codex_app_server_protocol.v2.schemas.json` (515 definitions)
- 本地备份: `/root/course/tmp_file/codex_research/protocol_schema_v1.json/`

## 附录 B: 实验验证记录

**实验**: 单 App Server 多 Workspace 可行性
**日期**: 2026-07-10
**结果**: ✅ 可行

```
App Server 启动 cwd: /tmp/codex_test/ws1
Thread 1: thread/start {cwd: "/tmp/codex_test/ws1"} → ✅
Thread 2: thread/start {cwd: "/tmp/codex_test/ws2"} → ✅（不同于启动 cwd）
```

AGENTS.md 加载验证（`codex debug prompt-input`）:
```
<INSTRUCTIONS>
  [CODEX_HOME/AGENTS.md]  ← 全局层
  --- project-doc ---
  [{cwd}/AGENTS.md]       ← 跟着 cwd 动态变化
</INSTRUCTIONS>
<environment_context>
  <cwd>{当前 cwd}</cwd>   ← 动态注入
</environment_context>
```

## 附录 C: 客户端代码骨架

```go
type CodexClient struct {
    proc     *exec.Cmd
    stdin    io.WriteCloser
    stdout   *bufio.Scanner
    pending  map[string]chan *JSONRPCMessage  // id → response channel
    notifyCh chan *JSONRPCMessage              // notifications
    approvalCh chan *JSONRPCMessage            // server requests (approvals)
}

func (c *CodexClient) Request(ctx context.Context, method string, params any) (*JSONRPCMessage, error) {
    id := nextID()
    msg := JSONRPCRequest{ID: id, Method: method, Params: params}
    
    ch := make(chan *JSONRPCMessage, 1)
    c.pending[id] = ch
    
    c.writeJSON(msg)
    
    select {
    case resp := <-ch:
        return resp, nil
    case <-ctx.Done():
        return nil, ctx.Err()
    }
}

func (c *CodexClient) readLoop() {
    for c.stdout.Scan() {
        msg := parseJSON(c.stdout.Text())
        
        switch {
        case msg.ID != nil && msg.Result != nil:
            // Response to our request
            c.pending[*msg.ID] <- msg
        case msg.ID != nil && msg.Method != "":
            // Server request (approval)
            c.approvalCh <- msg
        case msg.Method != "":
            // Notification
            c.notifyCh <- msg
        }
    }
}
```

---

> **文档结束**
> 本文档基于 Codex CLI 0.143.0/0.144.1 实际调研 + 协议 Schema 分析 + 实验验证。
