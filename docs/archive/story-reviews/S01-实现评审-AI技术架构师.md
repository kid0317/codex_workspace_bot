# S01 实现评审：AI 技术架构师

> 日期：2026-07-11｜范围：当前实现、S01 设计与 HLD｜结论：不建议标记 Delivered。

## 阻塞项

| 问题 | 证据 | 影响与建议 |
|---|---|---|
| 非版本化迁移 | `internal/storage/storage.go` 仅重复执行 `001_initial.sql` | 后续 Story 的 ALTER/数据迁移无法安全演进。实现前向 migration runner 与 `schema_migrations`。 |
| 日志无保留上限 | `internal/logging/manager.go` 仅移动归档 | 长期运行必然增长。增加 `retention_days`，可选总量上限。 |
| 轮转并发与错误处理不可靠 | ticker 吞掉 `Check` 错误；Handler 写锁与关闭文件锁不同 | 切分期间可能丢日志或写关闭文件。统一 Writer/轮转锁并提供降级告警。 |

## 重要项

- ChatGroup upsert、Message 创建和去重未在一个短事务中完成，和 S01 设计不符；应收束为 `PersistIncoming`。
- Message 缺少 `processing` 状态，终态更新无条件；在 S02 前改为条件状态迁移。
- Receiver 无显式状态快照、退避重连和 readiness，单 App 可能永久失联但服务健康。
- 飞书发送未设置明确短 deadline；固定回复阶段至少应限制网络等待。
- `appctl` 只支持 legacy import，未实现 S01 承诺的 CRUD。
- README、Story 状态落后于已完成的真实 p2p/group 验证。

## 可延后项

Worker/队列、App Server、流式卡片、Langfuse 写入、附件与审批都属于已确认的后续范围，不构成 S01 阻塞。
