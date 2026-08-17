# Codex Workspace Bot

Codex Workspace Bot 是一个本机自用的飞书 Bot 编排器：用户在飞书单聊或群聊中发消息，Bot 按飞书 App 和频道找到对应的本机工作区，驱动长期运行的 `codex app-server --stdio` 执行任务，并把结果回传到飞书。

它不是公开 SaaS，也不是通用聊天 Webhook 服务。当前设计重点是个人本机、多飞书 App、多 workspace、同频道串行、MySQL 可追溯和 Codex 原生运行。

## 功能概览

- 飞书 WebSocket 接入，支持多个自建应用的 p2p/group 文本消息。
- 一个长期 Codex App Server 子进程服务多个 workspace。
- 同一飞书频道严格 FIFO，不同频道并行处理。
- `work` 模式使用飞书卡片展示进展和最终正文。
- `companion` 模式只在成功终态后按 `[[SEND]]` 分段发送纯文本。
- 支持 `/help`、`/status`、`/cancel`、`/stop`、`/new`、`/goal <目标>`。
- 支持附件输入、当前频道消息发送、飞书文档创建/读取、图片/文件发送。
- 支持 Agent 管理当前 App/频道/用户范围内的 Cron Prompt 和本机 Shell Script 定时任务。
- 可选接入自托管 Langfuse，记录明文业务 Trace；运行凭据仍会被剥离。

## 文档

- [文档索引](docs/README.md)
- [需求分析](docs/00-requirements-analysis.md)
- [PRD](docs/03-product-requirements.md)
- [概要设计](docs/02-redesign-high-level.md)
- [数据库建表与迁移](docs/04-database-schema.md)
- [Story List](docs/story/STORY_LIST.md)
- [开源安全清单](docs/open-source-readiness.md)

## 依赖

| 依赖 | 是否必需 | 说明 |
|---|---|---|
| Go 1.23+ | 必需 | 构建 `cmd/server` 和 `cmd/appctl`。 |
| Codex CLI | 必需 | 需要先在本机执行 `codex login`。 |
| Docker / Docker Compose | Linux 默认路径需要 | 用于启动仓库提供的 MySQL compose；macOS 原生路径不需要。 |
| MySQL 8.4 | 必需 | Linux 可由 Compose 启动；macOS 可由 Homebrew 原生启动。 |
| 飞书自建应用 | 必需 | 需要长连接、事件订阅和消息发送权限。 |
| Langfuse | 可选 | 只在启用 `observability.langfuse.enabled` 时需要。 |
| Redis | 不需要 | 当前默认架构是 MySQL + 进程内队列。 |

## 配置文件

真实配置文件不能提交到仓库。请从模板复制：

```bash
cp .env.example .env
cp config.yaml.template config.yaml
```

需要填写：

- `.env`
  - `MYSQL_ROOT_PASSWORD`
  - `CODEX_WORKSPACE_BOT_DB_PASSWORD`
  - `CODEX_WORKSPACE_BOT_ATTACHMENT_KEY_V1`
  - `CODEX_WORKSPACE_BOT_ACTION_RESULT_KEY_V1`
  - 如启用定时任务：`CODEX_WORKSPACE_BOT_SCHEDULE_PAYLOAD_KEY_V1`、`CODEX_WORKSPACE_BOT_SCHEDULE_OWNER_HMAC_KEY_V1`
  - 如启用 Langfuse：`LANGFUSE_PUBLIC_KEY`、`LANGFUSE_SECRET_KEY`
- `config.yaml`
  - MySQL 地址、库名、用户和 `password_env`
  - Codex 命令和超时
  - 附件目录和大小限制
  - `feishu_actions.enabled`
  - `schedule.enabled`、`scripts.enabled`
  - 可选 Langfuse Project 绑定信息

生成 32 字节 base64 key：

```bash
openssl rand -base64 32
```

这些 key 应保持稳定；不要每次启动重新生成，否则旧的附件引用、action 结果或历史调度数据可能无法读取。

## 数据库建表与迁移

建表和迁移 SQL 已提交在 [`migrations/`](migrations/) 目录。首次启动不需要手工导入 SQL；`cmd/server` 和 `cmd/appctl` 会自动按文件名顺序执行尚未应用的迁移，并在 MySQL 中维护 `schema_migrations` 表。

可以用下面的命令在配置好 MySQL 后触发一次迁移检查：

```bash
go run ./cmd/appctl list --config ./config.yaml
```

迁移文件清单和手工恢复注意事项见 [数据库建表与迁移](docs/04-database-schema.md)。

## 飞书应用准备

在飞书开放平台创建企业自建应用，并至少准备：

- 启用长连接。
- 订阅 `im.message.receive_v1`。
- 授予接收单聊/群聊消息、发送消息所需权限。
- 如使用卡片和文档能力，补充 interactive card、文件、图片、docx/Drive 相关权限。
- 把 Bot 加入需要使用的测试群，或在单聊中打开会话。

应用的 `App ID` 和 `App Secret` 不写入 YAML，而是通过 `appctl` 写入本机 MySQL。

## Quick Start

### macOS 原生交互式安装

macOS 用户不需要 Ubuntu、Multipass 或 Docker。先完成 Codex 登录和飞书应用准备，再执行：

```bash
./scripts/macos_native_setup.sh --check
./scripts/macos_native_setup.sh
```

初始化器会检查或安装 Homebrew Go/MySQL、初始化本机数据库、生成私有 `.env`、提示输入飞书凭据和 Workspace 路径，并通过 `appctl` 登记应用。飞书 App Secret 通过 `--secret-env` 传给 `appctl`，不会出现在命令历史或进程参数中。

后续使用原生控制器：

```bash
./macos_bot_controller.sh status
./macos_bot_controller.sh restart
./macos_bot_controller.sh logs
./macos_bot_controller.sh stop
```

控制器使用当前登录用户的 `launchd`，凭据仍只保存在本机 `.env`。

### Linux / 手工安装

1. 登录 Codex CLI：

   ```bash
   codex login
   ```

2. 加载本机环境变量：

   ```bash
   set -a
   . ./.env
   set +a
   ```

3. 启动 MySQL：

   ```bash
   docker compose up -d mysql
   docker compose ps
   ```

4. 创建一个 Bot App 配置：

   ```bash
   printf 'Feishu App Secret（输入时不显示）：'
   read -r -s AIPM_FEISHU_APP_SECRET
   printf '\n'
   export AIPM_FEISHU_APP_SECRET
   go run ./cmd/appctl create \
     --config ./config.yaml \
     --name my-bot \
     --app-id cli_xxx \
     --secret-env AIPM_FEISHU_APP_SECRET \
     --workspace-dir /absolute/path/to/workspace \
     --model gpt-5 \
     --effort medium \
     --enabled=true
   unset AIPM_FEISHU_APP_SECRET
   ```

   也可以从旧本机配置导入：

   ```bash
   go run ./cmd/appctl import-legacy-app \
     --config ./config.yaml \
     --legacy-config /absolute/path/to/old/config.yaml \
     --name my-bot
   ```

5. 构建并启动服务：

   ```bash
   ./bot_controller.sh build
   ./bot_controller.sh start
   ```

6. 验证服务：

   ```bash
   curl --fail http://127.0.0.1:8080/healthz
   curl --fail http://127.0.0.1:8080/readyz
   ```

   `healthz` 预期返回 `ok`。`readyz` 应显示 Codex App Server 和 enabled 飞书 receiver 的状态。

7. 在飞书中发送 `/status` 或普通文本消息，确认 Bot 能回复。

## 运行控制

Linux 本地服务使用仓库根目录的 `bot_controller.sh` 管理：

```bash
./bot_controller.sh build
./bot_controller.sh start
./bot_controller.sh restart
./bot_controller.sh stop
```

macOS 使用对应的原生控制器：

```bash
./macos_bot_controller.sh build
./macos_bot_controller.sh start
./macos_bot_controller.sh restart
./macos_bot_controller.sh stop
```

不要用 `nohup`、shell 后台命令或直接执行 `runtime/codex_workspace_bot` 替代控制器。Linux 控制器使用 user systemd，macOS 控制器使用当前登录用户的 launchd；两者都会构建到 `runtime/codex_workspace_bot` 并负责服务生命周期。

## App 管理

macOS 原生安装完成后，推荐用交互脚本继续添加 Workspace：

```bash
./scripts/macos_native_add_workspace.sh
```

它会检查现有 `.env`、`config.yaml`、Go、`appctl` 和 MySQL，隐藏读取 App Secret，防止重复名称或重复 App ID 覆盖旧配置，并在写入后回读核对，再通过 `./macos_bot_controller.sh restart` 刷新 receiver。模型与 reasoning effort 默认沿用现有 `$CODEX_HOME/config.toml`。脚本还会等待 `/readyz` 中的 receiver 数量与全部 enabled App 一致、且每个状态都是 `connected`，之后才会提示生效。新 App 会立即启用，一次可以连续添加多个 Workspace。

**一个飞书 App ID 只能绑定一个 Workspace。** 如果需要多个 Workspace，必须在飞书开放平台分别创建多个独立的企业自建应用，每个应用使用各自的 App ID 和 App Secret。

```bash
go run ./cmd/appctl list --config ./config.yaml
go run ./cmd/appctl enable --config ./config.yaml --name my-bot
go run ./cmd/appctl disable --config ./config.yaml --name my-bot
printf 'Feishu App Secret（输入时不显示）：'; read -r -s AIPM_FEISHU_APP_SECRET; printf '\n'; export AIPM_FEISHU_APP_SECRET
go run ./cmd/appctl update --config ./config.yaml --name my-bot --app-id cli_xxx --secret-env AIPM_FEISHU_APP_SECRET --workspace-dir /abs/ws --model gpt-5 --effort medium
unset AIPM_FEISHU_APP_SECRET
go run ./cmd/appctl delete --config ./config.yaml --name my-bot
```

`list` 不会输出 App Secret。修改 App 配置后，重启服务使 receiver 配置生效。

## 定时任务

默认 `schedule.enabled=false`。启用前需要：

1. 在 `.env` 填写 schedule 相关 key。
2. 在 `config.yaml` 设置：

   ```yaml
   schedule:
     enabled: true
   ```

3. 如需允许 Script 任务，再设置：

   ```yaml
   scripts:
     enabled: true
     shell_path: "/bin/sh"
   ```

Script 会在对应 App 的 `workspace_dir` 下，以 Bot 当前 OS 用户执行 `shell_path -c command`。这是个人本机能力，不适合作为公开多用户执行环境。

## Langfuse

Langfuse 是可选依赖。启用前建议在自托管 Langfuse 中新建独立 Project，不复用旧项目 Key。填写 `.env` 中的 `LANGFUSE_PUBLIC_KEY`、`LANGFUSE_SECRET_KEY`，并在 `config.yaml` 中启用：

```yaml
observability:
  langfuse:
    enabled: true
    base_url: "https://langfuse.example.local"
    public_key_env: "LANGFUSE_PUBLIC_KEY"
    secret_key_env: "LANGFUSE_SECRET_KEY"
    project_id: "your-project-id"
    project_binding_nonce: "manual-readback-nonce"
    project_binding_verified: true
```

本项目的 Langfuse 策略是记录明文业务数据，便于个人排障；唯一禁止记录的是运行凭据，例如飞书 Key、Langfuse Key、Authorization、Cookie、access token、数据库密码和进程环境 secret。

## 本地验证

常规验证：

```bash
go test ./... -count=1
go vet ./...
go build ./cmd/server ./cmd/appctl
```

并发敏感改动建议补充：

```bash
go test -race ./internal/worker ./internal/codexapp ./internal/schedule -count=1
```

控制脚本验证：

```bash
bash scripts/test_bot_controller.sh
bash scripts/test_macos_native_setup.sh
bash scripts/test_macos_bot_controller.sh
```

真实飞书、真实 Codex App Server 和 Langfuse read-back 属于外部集成验收，需要用本机凭据单独执行并记录证据。

## 开源安全

提交前确认以下文件没有被 git 跟踪：

```bash
git ls-files .env config.yaml runtime logs .codex-workspace-bot graphify-out
```

这些路径已经在 `.gitignore` 中排除。更完整的检查见 [开源安全清单](docs/open-source-readiness.md)。

## 许可证

当前仓库尚未指定开源许可证。正式公开前请补充 `LICENSE`，并在本节写明许可类型。
