# 数据库建表与迁移

本项目提供 MySQL 建表和迁移 SQL，目录是仓库根目录下的 [`migrations/`](../migrations/)。

正常使用时不需要手工逐个执行这些 SQL。`cmd/server` 和 `cmd/appctl` 启动后都会调用同一套迁移逻辑，按文件名顺序执行 `migrations/*.sql`，并在数据库中维护 `schema_migrations` 表来记录版本和 checksum。已执行过的迁移如果文件内容发生变化，服务会拒绝继续启动，避免本机数据库状态和仓库迁移历史不一致。

## 首次建库流程

1. 复制并填写 `.env`、`config.yaml`。
2. 启动 MySQL：

   ```bash
   docker compose up -d mysql
   ```

3. 运行任意需要数据库的项目命令，例如：

   ```bash
   go run ./cmd/appctl list --config ./config.yaml
   ```

   这一步会自动创建 `schema_migrations`，然后执行 `migrations/` 下尚未应用的 SQL。

4. 创建 App 配置后启动服务：

   ```bash
   ./bot_controller.sh build
   ./bot_controller.sh start
   ```

## 迁移清单

| 文件 | 主要内容 |
|---|---|
| `001_initial.sql` | 初始表：`apps`、`chat_groups`、`messages`。 |
| `002_s04_companion_delivery.sql` | 为 `messages` 增加 companion 终态交付字段和 reconcile 索引。 |
| `003_s05_attachments_actions.sql` | 增加附件表 `attachments`、飞书 action 调用表 `feishu_action_calls`，并记录会话 toolset 版本。 |
| `004_s06_commands_time.sql` | 为命令消息增加接收时间、命令类型、payload 摘要和执行效果字段。 |
| `005_s06_scheduled_tasks.sql` | 增加定时任务、脚本定义、运行记录、交付记录和工具调用记录表。 |
| `006_s06_schedule_plaintext_storage.sql` | 为定时任务 payload 和运行输入增加明文字段。 |
| `007_s06_schedule_tool_result_plaintext.sql` | 为定时任务工具调用结果增加明文字段。 |
| `008_s08_langfuse_usage_ledger.sql` | 增加 turn usage 明细和 session usage 汇总表。 |
| `009_s08_thread_usage_snapshots.sql` | 增加 Codex thread usage 快照表。 |
| `010_s05_attachment_relative_path_utf8mb4.sql` | 调整附件相对路径字段为 `utf8mb4`，支持原始文件名。 |

## 手工执行说明

这些 SQL 可以作为建表语句直接阅读或用于一次性恢复环境，但不建议在服务正在使用的数据库上绕过项目迁移器手工执行。项目迁移器除了执行 SQL，还会记录 `schema_migrations.version` 和 checksum；绕过它可能导致后续启动时报迁移状态不一致。

如果确实需要人工恢复，应先备份数据库，再按文件名顺序执行所有迁移，并确认最终 `schema_migrations` 与仓库当前迁移文件一致。
