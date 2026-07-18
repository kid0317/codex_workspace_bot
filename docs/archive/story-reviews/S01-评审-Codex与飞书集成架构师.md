# Story 1 独立评审：Codex App Server × 飞书集成架构师

> **评审角色**: Codex App Server 与飞书集成架构师  
> **结论**: **有条件通过，不能直接进入实现**

## 审阅方法

评审者重新生成本机 `codex-cli 0.144.1` 的 App Server JSON Schema，核对 `ThreadStartParams`、`ThreadResumeParams`、`TurnStartParams`、审批和 sandbox 类型；检查飞书 Go SDK v3 事件/ACK/重连行为，并对照官方 [接收消息](https://open.feishu.cn/document/server-docs/im-v1/message/events/receive?lang=zh-CN) 和 [长连接](https://open.feishu.cn/document/server-docs/event-subscription-guide/event-subscription-configure-/request-url-configuration-case?lang=zh-CN) 文档。

## Blocking

### B1：AllowedChats 缺失

应用必须在解析正文、创建 ChatGroup/Message 和发送回复前，基于每 App 的 allowlist 校验。推荐 `app_allowed_chats` 表与 `appctl allowed-chat add/list/remove`；缺少规则的首次 p2p 授权机制也必须明确。默认允许全部会违反当前项目 AGENTS，不建议采用。

## Major

### M1：不要在 WebSocket handler 同步完成飞书发送

SDK 在 handler 返回后才 ACK；同步 DB/发送会延迟 ACK 或返回失败，导致重投、吞吐下降和重复窗口。

**修正**：回调仅完成字段验证、授权、ChatGroup upsert 和 Message 幂等落库后快速返回；最小有界内存执行器处理固定回复与最终状态。为 DB 和发送设置超时、状态条件更新；测试慢 Sender 不阻塞 ACK、并发重投只产生一次出站发送。

### M2：App 生命周期与 `appctl` 语义不闭合

启动时读取 enabled App，因此 `appctl disable` 不会自动停止已运行 receiver；有历史外键的 App 也不能直接删除。

**修正**：一期明确 enable/disable 需重启才生效，或实现 receiver manager 热加载；已使用 App 仅 disable/soft delete，物理 delete 仅无关联数据。加入 disable 后重启不连接、历史 App delete 被拒绝的测试。

### M3：校验事件 header 的 App 归属与 ID

V2 事件有 `header.app_id` 与 `header.event_id`。标准化时应验证前者与 receiver 的 `feishu_app_id` 一致；缺少 event ID 或不匹配都不落库、不回复，仅记录脱敏告警。幂等键明确来源为 `header.event_id`。

### M4：App Server future mapper 需准确

`reasoning_effort` 仅用于 `turn/start.effort`，不是 `thread/start` 字段；`turn/start.sandboxPolicy` 是对象。映射应单独实现并单测：

| DB `sandbox_mode` | Thread/Resume `sandbox` | Turn `sandboxPolicy` |
|---|---|---|
| `read-only` | `read-only` | `{type: readOnly, networkAccess: false}` |
| `workspace-write` | `workspace-write` | `{type: workspaceWrite, writableRoots: [], networkAccess: false}` |
| `danger-full-access` | `danger-full-access` | `{type: dangerFullAccess}` |

### M5：固定回复不能输出 raw chat_id

改用 App name、ChatGroup 本地 ID/截断 hash 与 `trace_id`；增加文本断言，禁止任意 reply 含原始飞书 ID、CWD、Secret 或正文。

## Minor

- UUID `trace_id` 可作 Langfuse external ID；后续 W3C traceparent 需转 32 位 hex trace ID，并生成独立 span ID。
- 定义 receiver 状态：connecting、connected、reconnecting、fatal、stopped；SDK 的 context 停止不应被误称完整优雅 stop。
- SDK chat type 还包括 `topic_group`；一期应显式忽略且不得复用普通 group ChatGroup。
- `request_timeout_seconds=30` 只用于 JSON-RPC 请求响应，不是长 Agent Turn 上限。

## 已确认契约

- `im.message.receive_v1` 提供 `message_id`、`chat_id`、`thread_id`、`chat_type`、`message_type` 和 JSON content；一期接 text + p2p/group 合理。
- p2p 使用 sender `open_id` + `receive_id_type=open_id` 发送；group 使用 `chat_id` + `receive_id_type=chat_id`。
- 每 enabled App 一个独立 WebSocket receiver 合理，但必须有 header App ID 防御性校验。
- `approval_policy=never` 与 `sandbox_mode=danger-full-access` 是有效 Thread 配置；ChatGroup 直接存 Codex Thread ID 和一轮一条 Message 也合理。

## 外部前置条件

每个真实 App 必须是支持长连接的企业自建应用，后台订阅 `im.message.receive_v1`，完成接收/发送消息权限、目标群成员关系及租户管理员授权。真实 smoke 需覆盖 p2p、group、重复事件、非 text、未授权、失效凭证、断线重连和发送失败。
