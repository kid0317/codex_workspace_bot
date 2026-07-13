# Codex Workspace Bot

本项目是本机自用的 Codex + 飞书工作区 Bot。S01/S02 已交付；S03 将同一条 Worker 主链路接到本 bot 独占的真实 Codex App Server stdio child，而不再回显模拟请求参数。

## 本机启动

前置条件：Docker Compose、Go 1.23 或以上、已登录的 `codex` CLI，以及一个可用的本地配置文件。先以 `codex login` 完成本机登录；若启动期 `initialize` 失败，修复登录/CLI/本机环境后重启 bot，不会自动循环重启。

1. 创建本机环境文件与服务配置（这两个文件已被 Git 忽略）：

   ```bash
   cp .env.example .env
   cp config.yaml.template config.yaml
   ```

   在 `.env` 中设置 `MYSQL_ROOT_PASSWORD`、`CODEX_WORKSPACE_BOT_DB_PASSWORD`，以及 S05 的两个稳定 AES-256 key（各执行一次 `openssl rand -base64 32` 后保存；不要每次启动重新生成）：

   ```bash
   set -a; . ./.env; set +a
   ```

2. 启动持久化 MySQL。数据库文件挂载在 `runtime/mysql-data/`；容器使用 `restart: unless-stopped`，机器重启后 Docker 服务启动时会自动恢复。

   ```bash
   docker compose up -d mysql
   docker compose ps
   ```

3. 从旧 CC Workspace Bot 配置导入一个应用。命令不会打印 Secret：

   ```bash
   go run ./cmd/appctl import-legacy-app \
     --config ./config.yaml \
     --legacy-config /root/cc_workspace_bot/config.yaml \
     --name health-assistant
   ```

4. 启动服务。启动时会幂等执行 `migrations/001_initial.sql`，创建并完成本 bot 独占 App Server child 的 `initialize`，随后才连接飞书 receiver：

   ```bash
   go run ./cmd/server --config ./config.yaml
   ```

5. 在另一个终端验证本机服务：

   ```bash
   curl --fail http://127.0.0.1:8080/healthz
   ```

   预期返回 `ok`。

## 日志

服务自行管理日志，无需配置 `cron` 或 `logrotate`：

- 普通服务日志：`logs/server.log`
- 工作流日志（为后续 Worker/App Server 预留）：`logs/server.log.wf`
- 每小时切分为 `server-YYYYMMDDHH.log` 与对应 `.wf` 文件。
- 服务后台任务会把早于今天的已切分日志移至 `logs/YYYYMMDD/`；服务重启后也会补偿执行。

配置示例：

```yaml
logging:
  level: info # debug | info | error
  dir: logs
```

`logging.level=debug` 是 S03 的本机验收开关。每个 child generation 会在 `logs/` 写入三份同步证据：

- `appserver-raw-<process-start>.ndjson`：完整 server→client 原始 JSON-RPC 行；
- `appserver-event-<process-start>.jsonl`：dispatch 前的分类、路由快照与 `seq`；
- `appserver-outcome-<process-start>.jsonl`：同一 `seq` 的 dispatch 结果。

它们只用于当前本机调试，不会发送到飞书、MySQL 或 Langfuse。测试完成后把级别改回 `info`；运行中 replacement 初始化失败时，新的入站消息会被持久化为 `app_server_unavailable` 并获得固定失败文案，人工修复后重启服务。

## 本地验证

```bash
go vet ./...
go test ./...
go test -race ./internal/...
```

S04 已交付 work/companion 输出：work 使用同一 CardKit entity 的全量 `Card.Update(card_json)` 更新进展与正文；若单次实体更新失败，只对该次同一 message 降级 PATCH，后续更新仍继续尝试 CardKit。companion 不创建卡片，只在成功终态后按 `[[SEND]]` 发送 plain-text 分段。`/cancel`/`/stop` 通过 TerminalArbiter 与 DeliverySlot 阻止尚未发布的 companion 首段，并等待交付槽收尾；workflow JSONL 写入失败会停止后续段并留下稳定失败状态。

S05 已交付：image/file 会先按原会话 FIFO 下载，再把实际本机路径交给 App Server；图片另以 `localImage` 传入。个人本机模式不设目录白名单或显式附件 chmod：`attachments.root_dir` 可为相对或绝对路径，当前会话的文件上传与 Markdown 文档创建可读取任意本机普通文件。文件发送会按内容签名识别 JPEG、PNG、GIF、WEBP、TIFF、BMP、ICO：符合飞书 image API 且不超过 10 MB 时发送为原生图片，其他内容发送为文件。当前会话的受控动态 action 还支持发消息、创建并公告 Markdown 飞书文档，以及读取当前 App 有权访问的 docx 文档。仍保留非空文件、regular-file、30 MB 上限和当前会话回复目标约束。附件 retention 会在启动及每小时执行；动态 action 结果使用 `.env` 中的稳定 key 加密缓存。
