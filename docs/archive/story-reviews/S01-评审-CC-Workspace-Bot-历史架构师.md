# Story 1 独立评审：CC Workspace Bot 历史架构师

> **评审角色**: CC Workspace Bot 历史架构师  
> **结论**: **拒绝通过；先关闭两个阻断问题**

## 审阅来源

新项目的 Story/HLD；历史项目的 README、`docs/design.md`、`docs/requirements.md`、`config.yaml.template`、`internal/config`、`internal/db`、`internal/model`、`internal/feishu`、`internal/session` 和测试；并检查了按 workspace 拆库、Langfuse/陪伴模式、孤儿 Session 回填相关历史提交。

## Blocking

### B1：未继承 AllowedChats 的强制隔离

历史 `AppConfig` 包含 `allowed_chats`，Receiver 在分发前调用 `AllowedChat(chatID)`；新项目 AGENTS 仍把它列为硬约束。新设计没有对应表或字段。

**建议**：增加 `allowed_chat_mode`（`allowlist` / `all`）与 `app_allowed_chats(app_id, chat_id, chat_type, created_at)`；默认 `allowlist`，测试 App 可以显式 `all`。Router 必须在创建 ChatGroup、Message 与投递之前校验；为未授权 p2p/group 增加“无业务记录、无回复、无正文日志”的验收。

### B2：未落实 `channel_key` 严格串行

旧项目每个 channel 懒建单 Worker、队列深度 64，解决 `/new`、持久化、模型和输出交错。新设计把 Worker/队列列为非目标，而飞书 SDK handler 可并发。

**建议**：Story 1 实现最小按 `channel_key` 串行执行器，不持久化 Worker 但有 20 全局上限、64 队深和可追踪的 `rejected`；并测试同键有序、异键并行、满载不静默丢弃。

## Major

1. **单坏 App 不应拖垮全部 App**：DB/迁移失败应 fail-fast；单 App 飞书连接失败应隔离为 `degraded` 并退避重连。健康检查需区分进程存活与所有 enabled App 不可用。
2. **幂等唯一键加入 App 范围**：即使飞书当前 ID 通常全局唯一，也不应固化该外部假设；改为 `UNIQUE(app_id, feishu_event_id)`、`UNIQUE(app_id, feishu_user_message_id)`，`trace_id` 继续全局唯一。
3. **固化飞书事件夹具**：脱敏 p2p/group JSON fixture 必须断言 ChatGroup 使用 `message.chat_id`，p2p 回复使用 `sender.open_id`，group 回复使用 `chat_id`，避免重犯历史设计中的 ID 混用。

## Minor 与历史债务

- 固定回复不可展示真实 chat_id。
- 启动时把遗留 `received|processing` 标为 `failed/process_interrupted`，不重放但保持审计闭环。
- 配置加载要测试环境变量缺失/空值/非法覆盖，日志只显示变量名。
- 附件大小限制、文件名净化、会话目录隔离、卡片失败 text fallback、分段与限流重试都可延后，但应在附件/输出 Story 开始前恢复。
- 旧 JSONL/Langfuse 成本解析不应复用；新 metadata-only + fail-open 方向更安全。

## 已保留的正向选择

从 SQLite/每 workspace DB 改为 MySQL + App 外键符合新方向；`ChatGroup.codex_thread_id` 取代旧 Claude Session 双层状态更简洁；一期只做 text 固定回显且不伪称模型验收，边界正确；每 App 一个 WS client、p2p/open_id 与 group/chat_id 的路由也与旧可运行 Receiver 一致。

## 判定

**修复 AllowedChats 与同渠道串行后才可以实现。**
