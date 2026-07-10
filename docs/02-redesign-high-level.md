# Codex Workspace Bot 概要设计 v2

> **版本**: v2.0 | **日期**: 2026-07-10
> **定位**: 企业级飞书 AI 助理平台 — Codex 原生版本
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
| **单进程** | 一个 App Server 进程服务所有 workspace |
| **简化存储** | MySQL 持久化 + 内存队列，不用 Redis |
| **流式优先** | 所有 AI 输出通过 Notification events 流式驱动飞书卡片 |
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

### 2.1 实体关系图

```
┌─────────────────────────────────────────────────────────────────┐
│                         实体关系                                  │
│                                                                  │
│  ┌──────────┐                                                    │
│  │   App    │  ← 飞书应用（一个 app_id）                          │
│  │ 1:1      │                                                    │
│  └────┬─────┘                                                    │
│       │                                                          │
│       │ 1:N                                                      │
│       ▼                                                          │
│  ┌──────────┐                                                    │
│  │ Channel  │  ← 一个 chat_id（单聊/群聊/话题群）                  │
│  │ 1:1      │                                                    │
│  └────┬─────┘                                                    │
│       │                                                          │
│       │ 1:1                                                      │
│       ▼                                                          │
│  ┌──────────┐       ┌──────────────┐                            │
│  │  Worker  │ ────► │   Session    │ ← Codex Thread              │
│  │ (队列)   │       │ engine_      │                             │
│  │          │       │ thread_id    │                             │
│  └──────────┘       └──────────────┘                            │
│                                                                  │
│  ┌──────────┐                                                    │
│  │Workspace │  ← App 1:1 绑定，不同 Channel 共享同一 Workspace     │
│  │ (目录)   │     但 Thread ID 不同                               │
│  └──────────┘                                                    │
└─────────────────────────────────────────────────────────────────┘
```

### 2.2 实体详细说明

#### App（飞书应用）

```go
type App struct {
    ID              string    // "investment-assistant"
    FeishuAppID     string    // "cli_xxx"
    FeishuAppSecret string    // 飞书密钥
    WorkspaceDir    string    // "/data/workspaces/investment"
    WorkspaceMode   string    // "work" / "companion"
    AllowedChats    []string  // 白名单 chat_id（空=不限）
    Model           string    // "codex-mini" / "gpt-5.5"（per-app 覆盖）
    Effort          string    // "low" / "medium" / "high"
}
```

- 一个 App = 一个飞书自建应用
- 一个 App 绑定一个 Workspace 目录
- 不同 App 可以配不同模型（如投资用 gpt-5.5，陪伴用 codex-mini）

#### Channel（聊天渠道）

```go
type Channel struct {
    ChannelKey string    // "p2p:{chat_id}:{app_id}" 或 "group:{chat_id}:{app_id}"
    AppID      string    // 所属 App
    ChatType   string    // "p2p" / "group" / "topic_group"
    ChatID     string    // 飞书 chat_id
    ThreadID   string    // 话题 ID（仅话题群）
}
```

- 一个 App 可以有多个 Channel（多个群聊/单聊）
- Channel 是消息路由和串行调度的基本单位
- 同一 App 下不同 Channel 共享 Workspace，但有独立的 Session/Thread

#### Worker（渠道工作器）

```go
type Worker struct {
    channelKey  string
    queue       chan *Message  // 内存队列，深度 64
    session     *Session       // 当前 Session
    lastActive  time.Time      // 最后活跃时间
}
```

- 每个 Channel 对应一个 Worker
- Worker 串行处理该 Channel 的所有消息
- **全局 Worker 池上限 20 个**（懒启动，空闲超时回收）
- 不同 Channel 的 Worker 并行工作

#### Session（Codex Thread）

```go
type Session struct {
    ID              string    // UUID
    ChannelKey      string    // 所属 Channel
    EngineThreadID  string    // Codex Thread ID
    Status          string    // "active" / "archived"
    CreatedBy       string    // open_id
}
```

- 一个 Channel 同一时刻只有一个 active Session
- Session 绑定一个 Codex Thread ID
- `/new` 命令归档当前 Session，创建新 Session + 新 Thread

### 2.3 路由规则

```
飞书消息到达
  │
  ├─ 解析 app_id（从 WS 事件识别）
  ├─ 查找 App 配置
  ├─ 构造 channel_key = "{chat_type}:{chat_id}:{app_id}"
  │
  ├─ 检查 AllowedChats（白名单过滤）
  │
  └─ 获取或创建 Worker（懒启动，上限 20）
       │
       └─ Worker.queue <- message
```

### 2.4 Worker 池管理

```go
type WorkerPool struct {
    workers   sync.Map        // channelKey → *Worker
    active    atomic.Int32    // 当前活跃 Worker 数
    maxWorkers int            // 上限 20
    mu        sync.Mutex      // 保护创建
    wg        sync.WaitGroup  // 优雅关闭
}
```

- **懒启动**: 收到消息时才创建 Worker
- **空闲回收**: 30 分钟无消息 → Worker 退出 → Session 归档
- **上限保护**: 超过 20 个 → 拒绝新 Worker（返回错误提示）
- **优雅关闭**: 等待所有 Worker 完成当前任务后退出

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
│                      会话层 (Session)                            │
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
  ├── HTTP Server (/health, /debug/*)
  ├── Worker Pool (max 20)
  │     └── 每个 Worker 一个 goroutine
  ├── Task Scheduler (gocron)
  ├── Cleanup Cron
  │
  └── 子进程: codex app-server --stdio
        └── 单进程服务所有 workspace
```

---

## 4. 消息处理流程

### 4.1 标准消息处理

```
飞书 WS 事件
  │
  ▼
Receiver.parse()
  ├─ 识别 app_id（从 WS 连接识别）
  ├─ 解析消息类型（text/image/file/post）
  ├─ 下载附件 → tmp/
  ├─ 构造 channel_key
  └─ 检查 AllowedChats
  │
  ▼
Router.dispatch(msg)
  ├─ 查找 App 配置
  ├─ 获取或创建 Worker（懒启动，上限 20）
  └─ Worker.queue <- msg
  │
  ▼
Worker.process(msg)
  ├─ 解析命令（/new /cancel /status）
  │   └─ 是命令 → 执行命令 → return
  │
  ├─ getOrCreateSession()
  │   └─ 无 active session → Engine.startThread(cwd=workspaceDir)
  │
  ├─ moveAttachments() → sessions/<id>/attachments/
  ├─ recordMessage(user) → MySQL
  │
  ├─ Engine.startTurn({
  │     threadId: session.threadId,
  │     cwd: app.WorkspaceDir,
  │     input: [text],
  │     model: app.Model,      // per-app 覆盖
  │   })
  │
  ├─ 返回 EventStream
  │
  └─ 处理事件流
      ├─ turn/started → 发送"思考中"卡片
      ├─ item/agentMessage/delta → 流式 PATCH 卡片
      ├─ item/commandExecution/* → 可选展示
      ├─ approval_requested → 审批卡片
      └─ turn/completed → 最终 PATCH + persist
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
| `turn/started` | 开始流式输出 | P0 |
| `item/agentMessage/delta` | **文本流式输出**（核心） | P0 |
| `turn/completed` | 结束 + Token 统计 | P0 |
| `error` | 错误处理 | P0 |
| `item/commandExecution/requestApproval` | 审批流 | P0 |
| `item/fileChange/requestApproval` | 文件变更审批 | P0 |
| `item/permissions/requestApproval` | 权限审批 | P0 |
| `thread/tokenUsage/updated` | Token 用量（Langfuse） | P1 |
| `item/reasoning/textDelta` | 推理过程（可选展示） | P2 |
| `hook/started` / `hook/completed` | Hook 执行（Langfuse） | P2 |

### 5.6 可忽略的 Notification Events

| 事件 | 理由 |
|------|------|
| `thread/started` | 不需要，thread/start 已同步返回 |
| `thread/name/updated` | 飞书场景不需要 |
| `thread/goal/*` | 飞书场景不需要 |
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
旧版:
  消息 → "⏳ 思考中" 卡片 → 等待完整 → 一次性 PATCH

新版:
  消息 → "⏳ 思考中" 卡片 → 逐段 PATCH → 最终状态
  
PATCH 策略:
  - 首个 delta → 立即 PATCH（切换到内容模式）
  - 后续 delta → 每 200ms 或 50 字符（先到为准）
  - 最后 delta → 立即 PATCH（完成状态）
```

### 6.2 Companion 模式流式分段

```
旧版:
  等待完整 → 检测 [[SEND]] → 分段 → 打字延迟 → 发送

新版:
  流式 delta 到达 → 实时拼接
    ├─ 检测到 [[SEND]] → 立即发送当前段 + 打字延迟
    └─ 未检测到 → 继续累积
  turn/completed → 发送剩余段
```

### 6.3 飞书卡片模板

#### Work 模式

```json
{
  "elements": [
    {
      "tag": "markdown",
      "content": "## 🤖 投资助理\n---\n正在分析..."
    },
    {
      "tag": "action",
      "actions": [
        {"tag": "button", "text": {"content": "⏹ 停止"}, "value": {"cmd": "cancel"}},
        {"tag": "button", "text": {"content": "🔄 重新生成"}, "value": {"cmd": "retry"}}
      ]
    },
    {
      "tag": "note",
      "elements": [
        {"tag": "plain_text", "content": "⏱ 3.2s | 245 tokens | gpt-5.5"}
      ]
    }
  ]
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

| 命令 | 说明 | 实现 |
|------|------|------|
| `/new` | 开启新会话 | 归档当前 session → 新 Thread |
| `/cancel` | 取消当前 turn | `turn/interrupt` |
| `/status` | 查看状态 | `account/rateLimits/read` + `account/usage/read` |
| `/help` | 显示帮助 | 静态文本 |

### 7.2 /status 命令实现

```go
func (w *Worker) handleStatus(ctx context.Context, msg *Message) {
    // 1. 查询账户信息
    account, _ := w.engine.Request(ctx, "account/read", nil)
    
    // 2. 查询速率限制
    rateLimits, _ := w.engine.Request(ctx, "account/rateLimits/read", nil)
    
    // 3. 查询用量
    usage, _ := w.engine.Request(ctx, "account/usage/read", nil)
    
    // 4. 查询当前 thread 信息
    threadInfo := w.session.Info()
    
    // 5. 组合回复
    reply := fmt.Sprintf(`📊 **状态信息**
    
🤖 当前模型: %s
🧵 Thread ID: %s
📝 Session 状态: %s
⏱ 已运行: %s

📈 **用量**:
  - 今日 Token: %d
  - 今日请求: %d

⏳ **速率限制**:
  - 剩余请求: %d/%d
  - 重置时间: %s`,
        w.app.Model,
        threadInfo.ID,
        w.session.Status,
        threadInfo.Duration,
        usage.TodayTokens,
        usage.TodayRequests,
        rateLimits.Remaining,
        rateLimits.Limit,
        rateLimits.ResetAt,
    )
    
    w.sender.SendText(ctx, msg, reply)
}
```

### 7.3 命令识别

```go
func parseCommand(text string) (cmd string, args string, isCmd bool) {
    text = strings.TrimSpace(text)
    if !strings.HasPrefix(text, "/") {
        return "", "", false
    }
    
    parts := strings.SplitN(text[1:], " ", 2)
    cmd = parts[0]
    if len(parts) > 1 {
        args = parts[1]
    }
    return cmd, args, true
}
```

---

## 8. Workspace 管理

### 8.1 目录结构（不变）

```
workspaces/<app-id>/
├── CLAUDE.md              # ← 直接被 Codex 加载（无需 AGENTS.md 桥接）
├── .claude/skills/        # Claude 遗留，Codex 不识别但不冲突
├── memory/                # 长期记忆（flock 保护）
├── tasks/                 # 定时任务 YAML
└── sessions/
    └── <session-id>/
        └── attachments/   # 附件
```

### 8.2 初始化

```bash
./init_workspace.sh <app-id> <workspace-dir> <feishu-app-id> <feishu-app-secret>
```

脚本行为:
1. 创建目录结构
2. 写入 feishu.json（0600 权限）
3. 复制模板文件
4. 追加 config.yaml

### 8.3 Per-App 模型配置

```yaml
apps:
  - id: "investment-assistant"
    model: "gpt-5.5"        # 投资用高端模型
    effort: "high"
    
  - id: "companion-mate"
    model: "codex-mini"     # 陪伴用轻量模型
    effort: "low"
    
  - id: "course-assistant"
    model: "gpt-5.5"        # 课程用高端模型
    effort: "medium"
```

---

## 9. 数据层设计

### 9.1 MySQL Schema

#### apps 表

```sql
CREATE TABLE apps (
    id VARCHAR(64) PRIMARY KEY,
    feishu_app_id VARCHAR(64) NOT NULL,
    feishu_app_secret VARCHAR(256) NOT NULL,
    feishu_verification_token VARCHAR(256),
    workspace_dir VARCHAR(512) NOT NULL,
    workspace_mode VARCHAR(16) DEFAULT 'work',
    model VARCHAR(64),
    effort VARCHAR(16),
    allowed_chats JSON,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_feishu_app_id (feishu_app_id)
);
```

#### channels 表

```sql
CREATE TABLE channels (
    channel_key VARCHAR(256) PRIMARY KEY,
    app_id VARCHAR(64) NOT NULL,
    chat_type VARCHAR(16) NOT NULL,
    chat_id VARCHAR(128) NOT NULL,
    thread_id VARCHAR(128),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_app_id (app_id)
);
```

#### sessions 表

```sql
CREATE TABLE sessions (
    id VARCHAR(36) PRIMARY KEY,
    channel_key VARCHAR(256) NOT NULL,
    engine_thread_id VARCHAR(64),
    workspace_dir VARCHAR(512),
    model VARCHAR(64),
    status VARCHAR(16) DEFAULT 'active',
    created_by VARCHAR(64),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_channel_key (channel_key),
    INDEX idx_status (status)
);
```

#### messages 表

```sql
CREATE TABLE messages (
    id VARCHAR(36) PRIMARY KEY,
    session_id VARCHAR(36) NOT NULL,
    sender_id VARCHAR(64),
    role VARCHAR(16) NOT NULL,
    content TEXT,
    feishu_msg_id VARCHAR(128),
    token_usage JSON,
    duration_ms INT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_session_id (session_id)
);
```

#### tasks 表

```sql
CREATE TABLE tasks (
    id VARCHAR(128) PRIMARY KEY,
    app_id VARCHAR(64) NOT NULL,
    name VARCHAR(128),
    cron_expr VARCHAR(64),
    target_type VARCHAR(16),
    target_id VARCHAR(128),
    prompt TEXT,
    enabled BOOLEAN DEFAULT TRUE,
    send_output BOOLEAN DEFAULT TRUE,
    created_by VARCHAR(64),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_run_at DATETIME,
    deleted_at DATETIME,
    INDEX idx_app_id (app_id),
    INDEX idx_deleted_at (deleted_at)
);
```

#### approvals 表（新增）

```sql
CREATE TABLE approvals (
    id VARCHAR(36) PRIMARY KEY,
    app_id VARCHAR(64),
    thread_id VARCHAR(64),
    turn_id VARCHAR(64),
    approval_type VARCHAR(32),
    detail_json JSON,
    decision VARCHAR(16) DEFAULT 'pending',
    decided_by VARCHAR(64),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    decided_at DATETIME,
    INDEX idx_thread_id (thread_id),
    INDEX idx_decision (decision)
);
```

### 9.2 内存队列

```go
// Worker 内部的消息队列
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
        return ErrQueueFull  // 队列满，拒绝
    }
}
```

---

## 10. 配置体系

### 10.1 config.yaml

```yaml
# ===== 应用列表 =====
apps:
  - id: "investment-assistant"
    feishu_app_id: "cli_xxx"
    feishu_app_secret: "xxx"
    feishu_verification_token: "xxx"
    feishu_encrypt_key: ""
    workspace_dir: "/data/workspaces/investment"
    workspace_mode: "work"
    allowed_chats: []
    model: "gpt-5.5"
    effort: "high"
    approval_policy: "untrusted"
    sandbox: "danger-full-access"
    
  - id: "companion-mate"
    feishu_app_id: "cli_yyy"
    feishu_app_secret: "yyy"
    workspace_dir: "/data/workspaces/companion"
    workspace_mode: "companion"
    model: "codex-mini"
    effort: "low"

# ===== Codex App Server 配置 =====
codex:
  binary: "codex"
  args: ["app-server", "--stdio"]
  timeout_minutes: 90
  max_turns: 300
  approval_timeout_minutes: 5

# ===== Worker 池配置 =====
worker:
  max_workers: 20
  idle_timeout_minutes: 30
  queue_depth: 64

# ===== 流式配置 =====
streaming:
  card_update_interval_ms: 200
  card_update_chars: 50
  companion_segment_delay_ms: 400

# ===== 数据层 =====
database:
  driver: "mysql"
  dsn: "user:password@tcp(127.0.0.1:3306)/codex_workspace_bot?charset=utf8mb4&parseTime=True"
  max_open_conns: 10
  max_idle_conns: 5

# ===== Langfuse =====
langfuse:
  enabled: true
  public_key: "pk-xxx"
  secret_key: "sk-xxx"
  host: "https://cloud.langfuse.com"

# ===== HTTP Server =====
server:
  port: 8080

# ===== 清理 =====
cleanup:
  attachments_retention_days: 7
  attachments_max_days: 30
  schedule: "0 2 * * *"
```

### 10.2 环境变量（仅数据库密码等）

```bash
# 数据库密码（不写入 config.yaml）
export DB_PASSWORD="xxx"

# Langfuse（如果不放 config.yaml）
export LANGFUSE_PUBLIC_KEY="pk-xxx"
export LANGFUSE_SECRET_KEY="sk-xxx"
```

---

## 11. 可观测性

### 11.1 Langfuse 集成（编排层直连）

**不用 hooks**，直接从 App Server Notification events 采集：

```go
type LangfuseReporter struct {
    client *langfuse.Client
}

func (r *LangfuseReporter) OnEvent(event *TurnEvent) {
    switch event.Type {
    case EventTurnStarted:
        r.startTrace(event)
    case EventDelta:
        r.appendGeneration(event)
    case EventCompleted:
        r.endTrace(event, event.InputTokens, event.OutputTokens)
    case EventFailed:
        r.endTraceWithError(event)
    }
}
```

### 11.2 可观测维度

| 维度 | 数据来源 | 用途 |
|------|---------|------|
| Token 用量 | `thread/tokenUsage/updated` | 成本分析 |
| Turn 延迟 | `turn/started` → `turn/completed` 时差 | 性能监控 |
| 错误率 | `error` 通知 | 异常告警 |
| 审批统计 | approvals 表 | 安全审计 |
| Worker 利用率 | 内存指标 | 容量规划 |

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

---

## 12. 功能清单

### 12.1 必须实现（P0）

| 功能 | 模块 | 说明 |
|------|------|------|
| 飞书 WS 接入 | feishu/ | 27 应用并发连接 |
| 消息解析 | feishu/ | text/image/file/post |
| 附件处理 | feishu/ | 下载 → 注入 prompt |
| channel_key 路由 | session/ | p2p/group/thread |
| Worker 池 | session/ | max 20，懒启动，空闲回收 |
| Session 管理 | session/ | 创建/归档/resume |
| Codex App Server 客户端 | codex/ | JSON-RPC + 事件流 |
| 流式卡片更新 | output/ | work 模式 |
| Companion 分段发送 | output/ | [[SEND]] + 打字延迟 |
| /new 命令 | command/ | 归档 + 新 Thread |
| /cancel 命令 | command/ | turn/interrupt |
| /status 命令 | command/ | account/rateLimits + usage |
| MySQL 持久化 | db/ | channels/sessions/messages/tasks |
| Workspace 初始化 | workspace/ | 模板复制 + feishu.json |
| 配置加载 | config/ | Viper + 校验 |

### 12.2 重要功能（P1）

| 功能 | 模块 | 说明 |
|------|------|------|
| 欢迎事件 | feishu/ | Bot 入群 / 用户入群 |
| AllowedChats 白名单 | router/ | 限制特定群聊 |
| 定时任务 | task/ | YAML → fsnotify → gocron |
| 审批代理 | approval/ | 飞书审批卡片 |
| Langfuse 集成 | observability/ | Token + 延迟 + 错误 |
| 长期记忆 | workspace/ | flock 保护 |
| 附件清理 | task/ | 定时 cron |

### 12.3 未来功能（P2）

| 功能 | 说明 |
|------|------|
| 百炼模型接入 | 需要 Proxy 层 |
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
 │        │         │         │        │         │←─delta─────│        │        │
 │        │         │         │        │         │←─delta─────│        │        │
 │        │         │         │        │         │            │         │        │
 │        │         │         │        │─PATCH card(delta)─────────────────────→│
 │←─卡片更新────────────────────────────────────────────────────────────────│
 │        │         │         │        │         │            │         │        │
 │        │         │         │        │         │←─completed─│        │        │
 │        │         │         │        │─record──────────────────→│        │        │
 │        │         │         │        │─PATCH card(final)────────────────────→│
 │←─最终卡片───────────────────────────────────────────────────────────────│
```

### A.2 /new 命令

```
User → /new
  │
  ▼
Worker.handleNew()
  ├─ 如果有活跃 turn → Engine.Interrupt()
  ├─ DB: session.status = 'archived'
  ├─ 清除 worker.session
  └─ 回复: "已开启新会话"
  
下一条消息:
  Worker.process()
  ├─ 无 session → Engine.startThread()
  │   └─ JSON-RPC: thread/start → 新 Thread ID
  ├─ 新 Session 写入 DB
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
  ├─ Engine.ensureThread(session.ThreadID)
  │   ├─ 尝试 thread/resume
  │   │   ├─ 成功 → 继续用旧 Thread
  │   │   └─ 失败 → thread/start 新建
  │   └─ 更新 Session.ThreadID
  └─ 正常处理
```

---

> **文档结束**
> v2.0 基于用户反馈重写：Codex 原生、不做迁移、不做引擎抽象、MySQL + 内存队列、Worker 池 20 上限、/status 命令、Langfuse 编排层直连。
