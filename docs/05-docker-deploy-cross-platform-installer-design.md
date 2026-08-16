# Docker Deploy：跨平台交互安装器与 Space 发行设计

> 版本：v0.2
>
> 日期：2026-08-16
>
> 状态：Sub Agent Review 后修订，进入实现与真实双平台验收
>
> 目标仓库：`codex_workspace_bot`

## 1. 一句话方案

在仓库新增 `docker-deploy/`，对用户只提供两个交互安装入口：

- macOS / Linux：`install.sh`
- Windows：`install.ps1`

两个脚本负责检查 Docker、询问安装路径、模型 Provider、Base URL、API Key、初始 Workspace 与飞书应用信息，然后生成相同结构的本地 Space，拉取发行镜像，并准备可重复使用的启动、停止、状态、日志、App 管理、更新和卸载脚本。

用户完成一次安装后，日常只需要进入 Space 目录运行：

```bash
./start.sh
```

或在 Windows PowerShell 运行：

```powershell
.\start.ps1
```

## 2. 本次设计的核心取舍

### 2.1 两个交互外壳，一个结果合同

Shell 和 PowerShell 负责各自操作系统的路径、隐藏输入、解压和文件权限；二者必须使用同一套模板、Schema、Provider preset、目录合同和验收用例。

不能把它做成两套各自演进的安装产品。相同回答在 macOS、Linux 和 Windows 上生成的非敏感配置应当语义一致。

### 2.2 一个安装包，四个隔离服务

用户体验是一份 Space 和一条启动命令；内部由 Compose 管理四个服务：

- `bot`：飞书 Receiver、Worker、MySQL、`appctl`、`spacectl` 和业务 Secret；不运行 Agent。
- `codex`：唯一 `codex app-server --stdio`、Workspace 与 `CODEX_HOME`；看不到飞书、DB、HMAC 与真实 Provider Key。
- `provider-proxy`：仅持有 Provider API Key，向固定上游转发 Responses 请求；不挂载 Workspace。
- `mysql`：MySQL 8.4，保存 App、会话、消息、任务和运行状态。

Redis 不是当前依赖，不进入镜像或 Compose。

Bot 通过一个无业务语义的 stdio/TCP bridge 驱动 Codex 容器。这个 bridge 保留当前“一个长期 App Server 服务多个 Workspace”的产品合同，同时把可执行 Shell 的 Agent 与 Bot Secret 放进不同进程、容器、挂载和网络边界。

### 2.3 Space 共享 Provider，多 App 各自绑定 Workspace

当前 Bot 只有一个长期运行的 `codex app-server --stdio`，因此一个 Space 共享：

- 一个 `CODEX_HOME`。
- 一套 Provider 与 API Key。
- 一个 Codex App Server 进程。

每个 App 独立保存：

- 飞书 App ID / Secret。
- Workspace 路径。
- work / companion 模式。
- model 和 reasoning effort。
- 飞书 Receiver、频道会话和 Codex Thread。

首版不承诺同一 Space 内按 App 隔离 Provider 或 Codex 登录状态。

## 3. 目标、非目标与前置条件

### 3.1 目标

1. Windows、macOS、Linux 生成同一套 Space。
2. 用户不需要本机安装 Go、Node.js、Codex CLI 或 MySQL。
3. 用户可以选择百炼、DeepSeek 或自定义 Responses Provider。
4. 用户可以在安装时导入零个、一个或多个 Workspace。
5. 用户可以以后用管理脚本继续新增 App。
6. 安装结束前完成 Compose 语法、Provider、Workspace 与 App 配置检查。
7. 镜像、Provider 模板和安装器都有明确版本，可升级和回滚。
8. 安装失败不能留下看似完整、实际不可启动的半成品 Space。
9. 用户可以用对等的 Shell/PowerShell 命令完成检查更新、升级、回滚准备和安全卸载。
10. Codex/Workspace 无法读取 Bot、其他 App 或 ACR 的 Secret。

### 3.2 非目标

- 不自动安装或启用 WSL、虚拟化、Docker Desktop，也不自动重启电脑。
- 不做 Kubernetes、云服务器或企业多租户部署。
- 不在首版支持 Chat/Completions-only Provider。
- 不让安装器自动申请飞书企业、权限或发布应用版本。
- 不在线修改 XiaoPaw 平台中的 Workspace。
- 不把 API Key、飞书 Secret 或数据库密码提交到 Git。
- 不承诺 ACR 个人版具备生产 SLA；课程与个人使用接受其开发测试定位。

### 3.3 用户前置条件

- Docker Desktop 或 Docker Engine 已安装并启动。
- `docker compose version` 可用。
- Windows 使用 Linux container backend，推荐 WSL 2。
- 用户已取得 Provider API Key。
- 若安装飞书 App，用户已取得 App ID、App Secret，并完成所需权限与长连接配置。

## 4. 当前实现事实与必须补的能力

| 当前事实 | 对安装器的影响 | 需要补的能力 |
|---|---|---|
| server 与 appctl 会自动执行 migrations | 不需要手工 SQL | 容器入口等待 MySQL 后继续自动迁移 |
| server 没有 enabled App 时退出 | 零 Workspace 不能直接启动 Bot | 安装器只准备 Space；新增 App 后再启动 |
| App 当前唯一事实在 MySQL | 只写 `app.yaml` 不会生效 | `spacectl reconcile` 读取 manifest 并幂等 upsert |
| 已有工作树正在增加 `appctl --secret-env` | 可以避免 Secret 进入 argv | `spacectl` 延续 env 引用并增加幂等文件 reconcile |
| 配置模板假定 Bot 在宿主机运行 | `127.0.0.1` 无法连接 Compose MySQL | 容器模板改为 `database.host=mysql` |
| 日志与附件使用相对本机路径 | 容器删除后可能丢失 | 统一写入 `/space/system/` 挂载目录 |
| 现有 Compose bind mount MySQL 数据 | Docker Desktop 跨平台体验不稳定 | 使用 Docker named volume |
| 单 app-server 服务多 Workspace | 可按 cwd 路由多个 App | Space 只能共享 Provider/CODEX_HOME |
| XiaoPaw 包可能含平台绝对路径 | 多 Workspace 无法共享 `/home/codex/workspace` | 增加导入检查和 runtime projection |
| App Server 当前继承 Bot 环境 | Agent Shell 可能读取跨 App Secret | Bot/Codex/Provider proxy 分容器，Bot child env 使用 allowlist |
| migration 包含不可简单反向的变更 | 切回旧镜像不等于数据库回滚 | update 前导出 MySQL；失败按 release 兼容声明决定切镜像或恢复数据 |

## 5. 仓库目录设计

实现后建议新增：

```text
docker-deploy/
├── README.md
├── VERSION
├── install.sh
├── install.ps1
├── image/
│   ├── Dockerfile
│   ├── entrypoint.sh
│   └── Dockerfile.dockerignore
├── templates/
│   ├── space/
│   │   ├── compose.yaml
│   │   ├── space.yaml
│   │   ├── bot.yaml
│   │   ├── env.example
│   │   └── lock.json
│   ├── app/
│   │   └── app.yaml
│   ├── providers/
│   │   ├── bailian-token-plan-responses/
│   │   │   ├── config.toml
│   │   │   └── models.json
│   │   ├── bailian-paygo-responses/
│   │   │   ├── config.toml
│   │   │   └── models.json
│   │   ├── deepseek-responses/
│   │   │   ├── config.toml
│   │   │   └── models.json
│   │   └── custom-responses/
│   │       ├── config.toml
│   │       └── models.json
│   └── runtime-scripts/
│       ├── start.sh
│       ├── stop.sh
│       ├── status.sh
│       ├── logs.sh
│       ├── manage.sh
│       ├── update.sh
│       ├── uninstall.sh
│       ├── start.ps1
│       ├── stop.ps1
│       ├── status.ps1
│       ├── logs.ps1
│       ├── manage.ps1
│       ├── update.ps1
│       └── uninstall.ps1
├── schemas/
│   ├── space.schema.json
│   ├── app.schema.json
│   └── install-answer.schema.json
├── release/
│   ├── build-multiarch.sh
│   ├── publish-acr.sh
│   ├── verify-manifest.sh
│   ├── release-manifest.json
│   └── release-manifest.json.sha256
└── tests/
    ├── fixtures/
    ├── golden/
    ├── test-install-sh.sh
    └── test-install-ps1.ps1
```

### 5.1 为什么模板独立于脚本

脚本只收集答案、选择模板和落盘。Compose、Bot 配置、Provider 配置与启动脚本都作为独立文件评审和测试，避免把大段 YAML/TOML 嵌入两份脚本后发生漂移。

### 5.2 换行符合同

仓库应增加 `.gitattributes`：

```gitattributes
*.sh text eol=lf
*.yaml text eol=lf
*.toml text eol=lf
*.json text eol=lf
*.ps1 text eol=crlf
*.cmd text eol=crlf
```

## 6. 生成后的 Space 目录

```text
codex-space/
├── compose.yaml
├── .env                         # 非敏感 Compose 版本/端口值
├── .env.example
├── .secrets/                    # 从不挂载给 Codex
│   ├── mysql.env
│   ├── bot.env
│   └── provider.env
├── config/
│   └── bot.yaml
├── space.yaml
├── space.lock.json              # 非敏感安装与版本清单
├── start.sh
├── stop.sh
├── status.sh
├── logs.sh
├── manage.sh
├── update.sh
├── uninstall.sh
├── start.ps1
├── stop.ps1
├── status.ps1
├── logs.ps1
├── manage.ps1
├── update.ps1
├── uninstall.ps1
├── apps/
│   ├── aipm-assistant/
│   │   ├── app.yaml
│   │   ├── source/              # 原始导出包，便于追溯
│   │   ├── workspace/           # 经过兼容检查的运行 Workspace
│   │   ├── user/                # Use 包可选
│   │   └── compatibility-report.md
│   └── another-app/
└── system/
    ├── codex-home/
    │   ├── config.toml
    │   ├── models.json
    │   └── ...Codex 状态
    ├── logs/
    ├── attachments/
    └── backups/
```

### 6.1 `space.lock.json`

只记录非敏感事实：

```json
{
  "schema_version": 1,
  "space_id": "cws-6f92b7d1",
  "installer_version": "0.1.0",
  "bot_image": ".../codex-workspace-bot@sha256:...",
  "mysql_image": "mysql:8.4.4",
  "provider_kind": "deepseek-responses",
  "provider_base_url": "https://api.deepseek.com",
  "codex_version": "0.147.0",
  "apps": ["aipm-assistant"]
}
```

它用于识别“这是受安装器管理的 Space”、升级判断和诊断，不记录 Key 或 Secret。

## 7. 完整交互流程

### 阶段 A：启动与预检

1. 显示安装器版本和即将执行的动作。
2. 检查当前 OS 与 CPU 架构。
3. 检查 `docker version`、`docker compose version` 和 Docker daemon。
4. 检查至少 10GB 可用磁盘空间。
5. Windows 检查 Linux container backend 与 WSL 版本；macOS 检查 Docker Desktop 是否已经启动；Linux 检查当前用户能否访问 Docker daemon。
6. 任何前置不满足时，只给出修复步骤和官方链接，不擅自修改系统功能。

### 阶段 B：选择安装位置

默认值：

- macOS/Linux：`$HOME/codex-space`
- Windows：`$env:USERPROFILE\codex-space`

规则：

- 支持空格、中文和 Unicode 路径，所有命令必须以数组/参数方式调用并正确引用。
- 新目录使用 staging 生成，成功后再落成正式 Space。
- 已有 `space.lock.json` 时进入“更新 / 新增 App / 修复”菜单。
- 目标是未知非空目录时停止，不覆盖用户文件。
- 为 Compose 生成唯一 `COMPOSE_PROJECT_NAME`，避免多个 Space 的容器与 volume 重名。
- Bot host 端口默认从 8080 开始探测，冲突时询问或选择下一个可用端口；MySQL 不向宿主机暴露端口。

### 阶段 C：选择 Provider

安装器展示四个首版选项：

1. 阿里百炼 Token Plan，Responses API。
2. 阿里百炼按量计费，Responses API。
3. DeepSeek 官方 Responses API。
4. 自定义 OpenAI Responses 兼容服务。

百炼 Coding Plan 当前仅提供 Chat/Completions 路线，最新版 Codex 只支持 `wire_api=responses`。如果用户选择 Coding Plan，安装器必须停止该分支并解释如何改用 Token Plan 或支持 Responses 的按量模型，不能自动降级 Codex 版本。

### 阶段 D：收集 Provider 信息

按选择预填并允许确认或修改：

| Provider | 默认 Base URL | 默认模型示例 | Key 环境变量 |
|---|---|---|---|
| 百炼 Token Plan | `https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1` | 发布包已验收的 Qwen Responses 模型 | `CODEX_PROVIDER_API_KEY` |
| 百炼按量 | 用户地域/Workspace ID 对应 URL | 发布包已验收的 Qwen Responses 模型 | `CODEX_PROVIDER_API_KEY` |
| DeepSeek | `https://api.deepseek.com` | `deepseek-v4-flash` / `deepseek-v4-pro` | `CODEX_PROVIDER_API_KEY` |
| 自定义 Responses | 用户输入 | 用户输入 | `CODEX_PROVIDER_API_KEY` |

交互字段：

- Base URL：显示预填值，允许编辑；必须为 `https://`，本机高级模式可显式接受 `http://localhost`。
- API Key：隐藏输入，两次不一致则重新输入；不回显、不写日志、不进入命令行参数。
- 默认 model：从本发行版验收清单选择，或进入高级自定义。
- reasoning effort：默认 `high`。
- 是否做最小 Provider 验证：默认是，并提示可能产生极少量调用费用。

生成的 `config.toml` 使用环境变量，不使用明文 token：

```toml
model = "deepseek-v4-flash"
model_provider = "deepseek"
model_reasoning_effort = "high"
model_catalog_json = "/space/system/codex-home/models.json"

[model_providers.deepseek]
name = "DeepSeek"
base_url = "https://api.deepseek.com"
env_key = "CODEX_PROVIDER_API_KEY"
wire_api = "responses"
```

### 阶段 E：是否安装初始 Workspace

安装器询问：

```text
现在安装一个 Workspace 吗？
[1] 是，添加第一个 App
[2] 暂不添加，只准备 Space
```

如果选择暂不添加：

- 仍然生成 Space、拉取镜像和准备管理脚本。
- 不启动 Bot，因为当前 server 要求至少一个 enabled App。
- 最后提示运行 `manage.sh` 或 `manage.ps1` 新增 App。

### 阶段 F：循环添加 App

每个 App 收集：

1. 显示名称，可使用中文。
2. 唯一 slug，仅允许 `[a-z0-9][a-z0-9-]{0,62}`。
3. Workspace 来源：目录或 ZIP。
4. 包类型：自动识别 XiaoPaw Dev、XiaoPaw Use 或普通 Workspace，用户确认。
5. 飞书 App ID，格式初检并要求非空。
6. 飞书 App Secret，隐藏输入。
7. 输出模式：`work` 默认，或 `companion`。
8. model：默认继承 Space，可为该 App 选择同 Provider 下另一模型。
9. reasoning effort：默认 `high`。
10. 是否启用。

完成一个后询问：

```text
还要继续添加 Workspace 吗？[y/N]
```

每个 App 的 Secret 写入 `.secrets/bot.env` 中的独立变量，例如：

```dotenv
FEISHU_AIPM_ASSISTANT_APP_ID=cli_xxx
FEISHU_AIPM_ASSISTANT_APP_SECRET=...
```

`app.yaml` 只引用变量名：

```yaml
schema_version: 1
id: aipm-assistant
display_name: AIPM 助手
enabled: true
package:
  kind: xiaopaw-use
  workspace: ./workspace
  user: ./user
codex:
  mode: work
  model: deepseek-v4-flash
  reasoning_effort: high
feishu:
  app_id_env: FEISHU_AIPM_ASSISTANT_APP_ID
  app_secret_env: FEISHU_AIPM_ASSISTANT_APP_SECRET
```

### 阶段 G：预览、确认与落盘

确认页只显示非敏感信息：

```text
安装位置：D:\AI\codex-space
Provider：DeepSeek Responses
Base URL：https://api.deepseek.com
默认模型：deepseek-v4-flash
Bot 端口：8080
App 数量：2
  - aipm-assistant / work / enabled
  - research-assistant / work / enabled
API Key：已填写（不显示）
飞书 Secret：2 个已填写（不显示）
```

用户确认后才开始文件写入和镜像拉取。

### 阶段 H：拉取镜像与可选首次启动

1. 运行 `docker compose config --quiet`。
2. 拉取固定版本或 digest 的 Bot 镜像和 MySQL 8.4 镜像。
3. 至少有一个 enabled App 时，询问“现在启动并验证吗？”。
4. 选择启动后：启动 MySQL、等待健康、运行 migration 与 `spacectl reconcile`、做 Provider 最小验证、启动 Bot、等待 `/readyz`。
5. 零 App 时不启动 Bot，只输出新增 App 和启动方法。

## 8. Workspace 导入设计

### 8.1 支持三种输入

| 输入 | 识别条件 | 处理 |
|---|---|---|
| XiaoPaw Dev ZIP/目录 | 根目录有 `README.md + workspace/` | 保留 source，复制 workspace 到运行目录 |
| XiaoPaw Use ZIP/目录 | 根目录有 `README.md + workspace/ + user/` | 保留 source，复制 workspace/user |
| 普通 Workspace 目录 | 用户指定目录本身就是 cwd | 复制到 `workspace/` |

首版统一“复制进 Space”，不生成指向外部目录的 bind mount。这样 Space 可以整体搬迁，也避免 Windows 盘符与共享盘权限进入 Compose。

### 8.2 ZIP 解压正确性

两个安装器都必须在解压前检查：

- 禁止绝对路径条目。
- 禁止 `..` 逃出目标目录。
- 解压到 App staging，不直接覆盖正式目录。
- 解压完成后重新检查根目录合同。
- 发现同名 App 时让用户选择更新配置、换新 slug 或取消，不能静默合并业务文件。

### 8.3 原包与运行投影分离

`source/` 保存原始导出包，`workspace/` 是实际运行投影。这样导入器可以修复生成型配置而不破坏原包，也能在出错时重新生成。

首版允许自动处理：

- 不启用导出包内的平台 `config.toml`，Provider 使用 Space 自己的配置。
- 忽略或替换 XiaoPaw 平台模型代理配置。
- 把可确定的生成型绝对路径替换成 `/space/apps/<slug>/...`。
- 为 Use 包保留 `user/`，并在兼容说明中给出相对访问路径。

首版不应盲目批量改写用户 Markdown、业务数据或任意脚本。对这些文件只扫描并写报告。

### 8.4 多 Workspace 的绝对路径问题

一个共享 app-server 无法让多个 App 同时把 `/home/codex/workspace` 指向不同目录。因此运行时统一使用：

```text
/space/apps/<slug>/workspace
/space/apps/<slug>/user
```

导入检查至少扫描：

- `/home/codex/workspace`
- `/home/codex/user`
- `/mnt/aipm/workspace`
- `/mnt/aipm/user`
- XiaoPaw 私有 Adapter/Proxy 地址
- 指向平台运行时的 `config_file` 绝对路径

发现未自动修复的活跃引用时，该 App 默认 `enabled: false`，并在 `compatibility-report.md` 中列出文件、引用类型和修复建议。

正式发布前必须用 XiaoPaw 导出的 AIPM 助手 Dev 包和 Use 包各跑一次真实导入。课程示例不能只靠结构推断通过。

## 9. Secret 与配置生成

### 9.1 Secret 分类

安装器询问：

- Provider API Key。
- 每个 App 的飞书 App Secret。

安装器自动生成：

- MySQL root password。
- MySQL Bot user password。
- 附件引用 32 字节 key。
- 飞书 action result 32 字节 key。
- 如果启用 schedule，再生成 schedule payload 与 owner HMAC key。

数据库密码使用不含换行和 dotenv 歧义字符的随机格式；业务 key 使用 32 字节安全随机数的 Base64。

### 9.2 平台实现

Shell：

- API Key/Secret 使用 `read -r -s`。
- 随机数优先使用 `openssl rand`，无 OpenSSL 时使用系统安全随机源。
- 退出和异常 trap 必须清除 staging 中尚未提交的 secret 文件。

PowerShell：

- API Key/Secret 使用 `Read-Host -AsSecureString`。
- 兼容 Windows PowerShell 5.1 与 PowerShell 7，不依赖仅在新版存在的明文转换参数。
- 随机数使用 .NET `RandomNumberGenerator`。
- 不使用 `Invoke-Expression`。

### 9.3 Secret 文件写入约束

- `.env` 只保存镜像 digest、端口、项目名等非敏感 Compose 变量。
- `.secrets/mysql.env` 只注入 MySQL；`.secrets/bot.env` 只注入 Bot；`.secrets/provider.env` 只注入 Provider proxy。
- Codex 容器不加载任何 `.secrets/*.env`，也不挂载 `.secrets`、Bot 配置或 MySQL 数据。
- Bot 只拿自己的飞书、数据库和业务 key，不拿 Provider 原始 API Key；Provider proxy 只拿 Provider Key，不拿飞书、数据库或 Workspace。
- 所有 Secret 文件都只允许单行值，发现换行立即拒绝。
- Key 不传到 `docker ... --env KEY=value`、程序 argv 或 release manifest 中。
- Secret 文件在 macOS/Linux 设为 `0600`；Windows 收紧为当前用户可读，并禁止安装到公共共享目录。
- 日志和确认页永不显示完整 Secret，只显示“未填 / 已填”。
- `.env`、`.secrets/`、备份 Secret、导入临时文件都在 `.gitignore` 范围内。

### 9.4 Agent Secret 隔离 Gate

仅靠文件权限无法防住能执行 Shell 的 Agent。发行版必须同时满足：

1. Bot 启动 `codex-remote` 时使用显式环境白名单；不得继承 Bot 全部环境。
2. `codex-remote` 只获得 bridge 地址、PATH、HOME、LANG 等最小运行变量。
3. `codex-bridge` 在独立 Codex 容器里启动唯一的 `codex app-server --stdio`。
4. App Server 的 Provider URL 指向内部固定上游的 proxy；Codex 只看到无价值的占位 token。
5. Provider proxy 删除来访认证头，再用自己的 Secret 注入真实上游认证；不记录请求头和请求体。
6. Canary E2E 必须从飞书消息触发 Agent，验证 `env`、`/proc/*/environ`、挂载目录和 Workspace 都读不到 Provider、飞书、DB、HMAC 或其他 App Secret。

## 10. `spacectl` 设计

跨平台脚本不自行实现 MySQL、Codex 与 Bot 业务规则。新增 Go 命令：

```text
spacectl validate
spacectl reconcile
spacectl doctor
spacectl backup
spacectl app list
```

### 10.1 `validate`

- 校验 `space.yaml` 与所有 `app.yaml` Schema。
- 路径解析后必须仍位于对应 App 根下。
- enabled App 必须有 Workspace、Provider model 与飞书 env 引用。
- 校验 Provider `config.toml` 和 `models.json` 可解析。
- 只报告 Secret 是否存在，不打印值。

### 10.2 `reconcile`

1. 按 slug 排序读取 App manifest。
2. 解析 env 引用。
3. 计算不含 Secret 明文的配置摘要。
4. 在事务中 upsert `apps`。
5. 文件管理的 App 被移除时只 disable，不删除会话和消息。
6. 重复运行相同配置不产生变化。

为区分文件管理与人工 DB 配置，增加 `app_bootstrap_state` migration，记录 app、source path、config hash 与 last seen，不存 Secret。

### 10.3 `doctor`

输出普通用户能理解的检查项：

```text
[通过] Space Schema
[通过] MySQL 连接与 migration
[通过] Codex CLI 版本 0.147.0
[通过] Provider Responses 最小调用
[通过] aipm-assistant Workspace 兼容检查
[通过] aipm-assistant 飞书配置完整
[通过] Bot /readyz
```

Provider 最小调用可能产生费用，首次安装前明确征得同意。离线 `doctor` 仍应能完成结构、环境变量和本地进程检查。

### 10.4 `backup`

把以下内容记录到 `system/backups/<timestamp>/`：

- MySQL dump。
- Space lock 与非敏感版本清单。
- 配置文件副本。
- App manifest。

不重复复制可能很大的 Workspace；备份报告说明 Workspace 已在 Space 主目录中。

## 11. Compose 与容器运行合同

```yaml
name: ${COMPOSE_PROJECT_NAME}

services:
  mysql:
    image: ${MYSQL_IMAGE}
    environment:
      MYSQL_ROOT_PASSWORD: ${MYSQL_ROOT_PASSWORD}
      MYSQL_DATABASE: ${MYSQL_DATABASE}
      MYSQL_USER: ${MYSQL_USER}
      MYSQL_PASSWORD: ${CODEX_WORKSPACE_BOT_DB_PASSWORD}
    volumes:
      - mysql-data:/var/lib/mysql
    healthcheck:
      test: ["CMD-SHELL", "mysqladmin ping -h 127.0.0.1 -u$$MYSQL_USER -p$$MYSQL_PASSWORD --silent"]
      interval: 5s
      timeout: 3s
      retries: 30
    restart: unless-stopped
    networks: [data]

  bot:
    image: ${BOT_IMAGE}
    env_file: ./.secrets/bot.env
    environment:
      CODEX_COMMAND: /usr/local/bin/codex-remote
      CODEX_BRIDGE_ADDR: codex:7070
      CODEX_CHILD_ENV_ALLOWLIST: PATH,HOME,LANG,LC_ALL,CODEX_BRIDGE_ADDR
    volumes:
      - ./apps:/space/apps:ro
      - ./attachments:/space/attachments
      - ./config/bot.yaml:/space/config/bot.yaml:ro
    depends_on:
      mysql:
        condition: service_healthy
      codex:
        condition: service_healthy
    ports:
      - "127.0.0.1:${BOT_HOST_PORT}:8080"
    stop_grace_period: 45s
    restart: unless-stopped
    networks: [data, control]

  codex:
    image: ${BOT_IMAGE}
    command: ["/usr/local/bin/codex-bridge"]
    environment:
      CODEX_HOME: /space/system/codex-home
      CODEX_BRIDGE_LISTEN: 0.0.0.0:7070
      CODEX_BRIDGE_HEALTH_LISTEN: 0.0.0.0:7071
    volumes:
      - ./apps:/space/apps
      - ./attachments:/space/attachments
      - ./system/codex-home:/space/system/codex-home
    healthcheck:
      test: ["CMD", "/usr/local/bin/codex-remote", "--health", "codex:7071"]
    stop_grace_period: 45s
    restart: unless-stopped
    networks: [control, model]

  provider-proxy:
    image: ${BOT_IMAGE}
    command: ["/usr/local/bin/provider-proxy"]
    env_file: ./.secrets/provider.env
    restart: unless-stopped
    networks: [model]

volumes:
  mysql-data:

networks:
  data: {}
  control: {}
  model: {}
```

MySQL、Codex bridge 和 Provider proxy 都不发布宿主机端口。Bot 只通过 `data` 网络访问 MySQL，通过 `control` 网络访问 bridge；Codex 只能通过 `model` 网络访问 Provider proxy。任何服务都不挂载 Docker socket。

Bot entrypoint 顺序为：

```text
预检挂载 → 等待 MySQL → migrations → spacectl validate/reconcile
→ exec server --config /space/config/bot.yaml
```

入口必须以 `exec` 让 server 成为主进程，并正确接收 SIGTERM。容器内不再使用宿主机专用的 `bot_controller.sh`。

## 12. macOS / Linux `install.sh`

### 12.1 支持环境

- macOS 默认 Bash 3.2 也能运行，不依赖 Bash 4 关联数组。
- Linux 支持 Bash；`sh install.sh` 不作为合同，必须使用 `bash install.sh`。
- 不依赖 `jq`、`yq`、Node.js、Python 或 Go。
- 可以依赖已安装的 Docker CLI 与 Compose plugin。

### 12.2 实现约束

- 开头使用 `set -Eeuo pipefail`，并为可预期的非零检查单独处理。
- 所有外部命令使用数组和带引号参数，不能拼接后 `eval`。
- 使用 `mktemp -d` 创建明确 staging。
- `trap` 只清理本次创建且经过前缀/父目录验证的 staging，不递归删除模糊路径。
- 复制目录优先使用系统自带工具；解压工具缺失时给安装提示。
- 完成后给生成的 `.sh` 添加执行权限。

### 12.3 启动方式

```bash
bash ./docker-deploy/install.sh
```

不推荐 `curl ... | bash`，因为用户应先看到发行包版本和校验值。

## 13. Windows `install.ps1`

### 13.1 支持环境

- Windows 10/11，Docker Desktop Linux containers。
- Windows PowerShell 5.1 与 PowerShell 7。
- 推荐 per-user Docker Desktop + WSL 2 backend。
- 不要求用户进入 Ubuntu，也不在 WSL shell 中运行安装器。

### 13.2 实现约束

- 使用 `Set-StrictMode -Version Latest` 和 `$ErrorActionPreference = 'Stop'`。
- 外部命令用参数数组和 `&` 调用，不使用字符串拼接或 `Invoke-Expression`。
- 路径使用 `Resolve-Path` / `[System.IO.Path]` 处理，正确支持空格和 Unicode。
- ZIP 使用 .NET `System.IO.Compression`，解压前验证每个 entry 的最终路径仍在 staging 内。
- 用 `try/finally` 清理 staging；只删除本次生成的精确目录。
- 保存 UTF-8 无 BOM 的 YAML/TOML/JSON，避免 Windows PowerShell 默认编码破坏配置。

### 13.3 启动方式

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\docker-deploy\install.ps1
```

`Bypass` 只作用于当前进程，不修改用户或系统的永久 Execution Policy。

## 14. Space 日常脚本

| 目的 | macOS/Linux | Windows |
|---|---|---|
| 启动 | `./start.sh` | `.\start.ps1` |
| 停止 | `./stop.sh` | `.\stop.ps1` |
| 状态 | `./status.sh` | `.\status.ps1` |
| 日志 | `./logs.sh` | `.\logs.ps1` |
| 新增/更新 App | `./manage.sh` | `.\manage.ps1` |
| 检查/执行更新 | `./update.sh` | `.\update.ps1` |
| 卸载 | `./uninstall.sh` | `.\uninstall.ps1` |

### 14.1 `start`

1. 检查 Docker daemon。
2. 检查至少一个 enabled App；没有则提示运行 manage 并退出。
3. `docker compose config --quiet`。
4. `docker compose up -d`。
5. 等待 health/ready，并显示每个 Receiver 状态。

### 14.2 `stop`

使用 `docker compose stop`。不使用 `docker compose down -v`，后者会删除 MySQL volume。

### 14.3 `manage`

复用安装器的 App 问卷和 Workspace 导入逻辑，完成后运行 `spacectl reconcile` 并询问是否重启 Bot。不能要求用户手工拼 `appctl --secret ...`。

### 14.4 `status` 与 `logs`

- `status` 同时显示 Compose 状态、Bot `/readyz`、Codex bridge、Provider proxy、MySQL 与每个飞书 Receiver；区分 `ready`、`degraded`、`stopped`。
- `logs` 默认显示 Bot 最近 200 行，可用参数选择服务和 follow；调用前检查服务名白名单，输出不得包含 env 或配置 Secret。

### 14.5 `update`

1. `--check` 只下载并验证 release manifest，不修改运行环境。
2. 校验 manifest 签名或随发行包提供的 SHA-256，再取得镜像 digest；不从未校验的 `latest` 升级。
3. 获取 Space 互斥锁，运行 doctor，并把 MySQL dump、配置和旧 lock 写到带时间戳的备份目录。
4. 按 digest 拉取候选镜像，运行 `docker compose config --quiet` 和离线兼容检查。
5. 切换镜像并启动，等待所有 readiness 和 Receiver 观察窗通过后才更新 `space.lock.json`。
6. 失败时恢复旧 manifest、Compose 和 digest；若候选版本执行了不兼容 migration，则停止服务并从本次 dump 恢复数据库，不能只切回旧镜像。
7. 保留最近 N 个成功备份，清理前显示精确路径；不运行全局 `docker system prune`。

### 14.6 `uninstall`

- 默认卸载只停止并 `docker compose down`，保留整个 Space、Workspace、Secret、附件和 MySQL volume，可再次启动。
- `--purge` 才删除受管 volume 和 Space 内生成物。执行前校验 `space.lock.json`、Compose label 和绝对路径，并要求用户完整输入 Space ID 二次确认。
- 不删除 Space 外文件，不跟随 symlink，不运行全局 prune，不删除其他 Compose project 的资源。
- purge 前默认生成最后一次 MySQL dump；用户显式选择“连备份一起删除”才删除备份，并说明不可恢复。

## 15. 重跑、升级与失败恢复

### 15.1 安装状态机

```text
NEW
  → PREFLIGHT_OK
  → ANSWERS_CONFIRMED
  → STAGING_RENDERED
  → CONFIG_VALIDATED
  → IMAGES_PULLED
  → SPACE_COMMITTED
  → OPTIONAL_STARTED
  → VERIFIED
```

只有进入 `SPACE_COMMITTED` 才把目录认作正式 Space。

### 15.2 重新运行安装器

发现 `space.lock.json` 后展示：

1. 新增 Workspace/App。
2. 修改 Provider。
3. 更新 Bot 镜像版本。
4. 修复缺失的模板与脚本。
5. 退出。

修改前先生成配置备份。Provider 变更会影响整个 Space，必须显示所有受影响 App，并在确认后执行。

### 15.3 安装失败

- 配置生成失败：删除 staging，正式目录不变。
- 镜像拉取失败：保留经过校验的 staging 或显式转换为“未完成安装”，给出重试命令；不能显示安装成功。
- MySQL 启动失败：保存 Compose 日志路径，不删除 volume。
- Provider 测试失败：允许用户返回修改 Base URL/model/Key；不打印 Key。
- 飞书 Receiver 失败：Space 可以保留，但结果标记“本地服务已启动，飞书尚未就绪”。

### 15.4 更新状态机

```text
IDLE → MANIFEST_VERIFIED → BACKUP_COMPLETE → CANDIDATE_PULLED
     → CONFIG_VALIDATED → SWITCHED → OBSERVING → COMMITTED
                                      └──失败→ ROLLBACK/DB_RESTORE → VERIFIED_OLD
```

release manifest 必须声明 Space Schema、DB migration 兼容范围、最低安装器版本和是否支持 N-1 直接回滚。没有兼容声明时，更新器按“可能需要恢复数据库”的保守路径处理。

## 16. 镜像发布设计

### 16.1 ACR

首版仓库已经准备为：

```text
crpi-0c1kby082wk3ovcx.cn-hangzhou.personal.cr.aliyuncs.com/
  codex-workspace/codex-workspace-bot
```

仓库类型已设为 `PUBLIC`。2026-08-17 使用空认证文件和纯 HTTP 无 Basic 凭据流程实测：匿名 pull token、总 manifest 与真实 ARM 大层均返回 200，blob 长度与 digest header 和发布清单一致；随后用全新空 `DOCKER_CONFIG` 按总 digest 执行 `docker pull` 成功。因此该具体仓库当前支持匿名拉取，不需要向学员分发 Registry 密码。Compose 固定 release manifest 中的 digest，不依赖 `latest`。

阿里云官方页面对新个人版实例强调的是独立域名、共享带宽、无固定出口带宽保障和高峰期限流，并没有写“Public 仓库不支持匿名拉取”。正式课程可以使用当前匿名地址，但要把并发控制、失败重试和未来策略变化后的备用渠道写入运行手册；ACR 个人版不宣称生产 SLA。

维护者侧使用阿里云 API 临时 Docker 凭据、临时 `DOCKER_CONFIG` 和 `docker login --password-stdin`；退出时清理临时目录。AK/SK、registry password、Docker auth JSON 都不进入 argv、日志、Git 或发行包。由于临时令牌有效期有限，`publish.sh` 分别发布 amd64、arm64，再合成总 manifest；每一步之前都可以刷新登录，不在一个长任务中赌令牌寿命。

### 16.2 架构

同一标签必须包含：

- `linux/amd64`：Windows x64、Intel Mac、常见 Linux x64。
- `linux/arm64`：Apple Silicon、Linux ARM64、Windows ARM64。

发布后用 `docker buildx imagetools inspect` 验证 manifest，再分别在 amd64/arm64 主机启动。

### 16.3 发布 Gate

公开镜像前必须满足：

- 项目尚无 LICENSE，因此 OCI license label 先用 `NOASSERTION`，发行说明明确“公开拉取不等于授予源码许可证”；许可证选择由维护者另行决定，安装器不得擅自声明 MIT 等许可证。
- 第三方 Notice 与镜像基础组件许可证清单。
- Bot/Codex/Space Schema/Git commit OCI labels。
- 无 Secret 构建上下文扫描。
- 以非 root 用户运行，镜像不包含编译器、源码、测试数据或 Docker CLI。
- release manifest、checksums、SBOM 与 provenance 随发行版保存；镜像 digest 进入 lock。
- amd64/arm64 冒烟测试。
- 使用无 Docker 登录状态按 digest 拉取，并验证 manifest 与真实 blob；课程集中安装时将并发控制在官方建议范围内。

## 17. 测试设计

### 17.1 安装脚本黄金用例

同一份 fixture 在 Shell 与 PowerShell 运行，去除随机值和平台换行后比较：

- `compose.yaml`
- `space.yaml`
- `bot.yaml`
- Provider `config.toml`
- App manifest
- `space.lock.json`

两端语义 diff 必须为零。

### 17.2 必测场景

| ID | 场景 | 预期 |
|---|---|---|
| INS-01 | 新 Space，不添加 App | 拉镜像并生成目录，不启动 Bot |
| INS-02 | 百炼 Token Plan + 一个 Dev 包 | Provider/Workspace/App 校验通过 |
| INS-03 | 百炼按量 + 自定义 Workspace ID URL | URL 与模型写入正确 |
| INS-04 | DeepSeek + Use 包 | Responses 配置、user 目录与模型目录正确 |
| INS-05 | 选择百炼 Coding Plan | 明确拒绝 chat-only 组合，不生成半成品 |
| INS-06 | 添加两个 App | 两条 manifest 与 env 引用独立，reconcile 幂等 |
| INS-07 | ZIP 含 `../` | 解压前拒绝，目标目录无越界文件 |
| INS-08 | 路径含空格/中文 | Shell 与 PowerShell 都成功 |
| INS-09 | API Key 含特殊但合法字符 | 不回显，不被模板或 dotenv 截断 |
| INS-10 | 目标未知非空目录 | 停止，不覆盖 |
| INS-11 | 重跑受管 Space | 进入 update/add-app/repair，不重置数据 |
| INS-12 | 镜像拉取中断 | 不宣称成功，可安全重试 |
| INS-13 | Docker daemon 未启动 | 给平台对应修复说明，不写正式 Space |
| INS-14 | Workspace 含平台绝对路径 | compatibility report 命中，App 默认不启用 |
| INS-15 | `start` 后重启 Docker Desktop | MySQL、Codex 状态、日志和 App 均保留 |
| INS-16 | 飞书消息要求读取 env/Secret | Agent 无法读取 Bot、Provider、DB、其他 App Secret |
| INS-17 | 更新后 readiness 失败 | 自动恢复旧 digest；必要时恢复本次 DB dump |
| INS-18 | 普通卸载 | 服务停止但 Space、volume、Workspace 和备份完整 |
| INS-19 | purge 输入错误 Space ID | 拒绝删除，其他 Compose project 完全不受影响 |

### 17.3 真实 E2E 矩阵

- Windows 11 x64 + Docker Desktop WSL 2。
- Apple Silicon Mac。
- Intel Mac；若没有长期设备，至少使用真实 amd64 runner 和一次人工桌面验收。
- Linux x64 Docker Engine。
- 百炼 Responses 真实最小请求。
- DeepSeek Responses 真实最小请求。
- XiaoPaw AIPM 助手 Dev/Use 各一次导入。
- 至少一个真实飞书 App 从消息进入到最终回复。

## 18. 分阶段实施建议

### Phase 1：镜像与容器运行基线

- 多阶段 Dockerfile、entrypoint、Compose。
- 固定 Codex 版本与双架构镜像。
- 容器内 MySQL、migration、app-server、SIGTERM 验证。
- 四服务 Secret trust boundary 与 canary E2E。

### Phase 2：Space Schema 与 `spacectl`

- app/space Schema。
- validate、reconcile、doctor、backup。
- `app_bootstrap_state` migration 和幂等测试。

### Phase 3：Workspace 导入

- Dev/Use/普通 Workspace 识别。
- ZIP 越界校验。
- source/runtime 分离与兼容报告。
- AIPM 助手真实导入 Gate。

### Phase 4：Shell 安装器

- macOS/Linux 问卷、staging、模板、拉取、启动和恢复。
- Apple Silicon、Intel/amd64、Linux 验收。

### Phase 5：PowerShell 安装器

- Windows 原生路径、SecureString、UTF-8、ZIP 与 Docker Desktop 检查。
- 与 Shell 黄金输出对齐。

### Phase 6：ACR 与教学发行

- LICENSE/Notice。
- 可匿名拉取的多架构公开仓库、Starter ZIP 和校验值；为个人版共享带宽准备分批安装和备用渠道。
- 面向小白的 Windows/macOS 教程与全流程录像。

## 19. 最终验收标准

1. 用户侧只有两个安装入口，Shell 与 PowerShell 生成同一合同。
2. 没装 Codex、Go、Node.js、MySQL 的电脑也能完成安装。
3. 百炼 Responses 与 DeepSeek Responses 都完成真实 Codex App Server 调用。
4. 选择不兼容的 chat-only 方案会在生成前被明确阻止。
5. 安装时可以添加零个、一个或多个 App。
6. 后续可以用 manage 脚本新增 App，不需要手工改数据库。
7. API Key 和飞书 Secret 不进入 Git、日志、确认页或命令参数。
8. Space 支持空格与中文路径。
9. Windows、Apple Silicon、Intel/amd64、Linux 至少各通过目标矩阵中的验证。
10. 两个 App 能同时连接各自飞书 Receiver，并各自在自己的 Workspace cwd 运行。
11. 重启 Docker Desktop 后 MySQL、Codex Home、Workspace、日志和附件仍在。
12. 安装中断、拉取失败和 Provider 配错均能恢复，不产生伪成功。
13. AIPM 助手 Dev/Use 导出包各通过一次真实导入与飞书消息 E2E。
14. 普通停止不删除 named volume；删除数据必须是独立、明确确认的动作。
15. ACR 个人版仓库可以在无登录状态按 digest 拉取，且 manifest 同时含 amd64/arm64；匿名 token、manifest 和真实 blob 均通过验证。
16. Agent 的环境、文件系统和 `/proc` 看不到 Bot、Provider、数据库、HMAC 或其他 App Secret。
17. 更新有已验证 manifest、DB 备份、观察窗和失败回滚；卸载默认保留数据，purge 必须二次确认。

## 20. 需求覆盖 Review

| 用户需求 | 本设计对应 | 状态 |
|---|---|---|
| 新建 Docker Deploy 目录 | `docker-deploy/` 完整仓库树 | 已设计 |
| Mac/Linux 与 Windows 两个脚本 | `install.sh`、`install.ps1` | 已设计 |
| 交互式收集 Provider、URL、Key | 阶段 C/D | 已设计 |
| 可选初始 Workspace | 阶段 E/F | 已设计 |
| 收集飞书 App ID/Secret | App 循环问卷 | 已设计 |
| 可以继续添加多个 Workspace | 安装循环 + manage | 已设计 |
| 自动建立指定路径的 Space | staging + 原子落盘 | 已设计 |
| 自动生成配置和 Compose | 独立版本化模板 | 已设计 |
| 自动拉取全部镜像 | 阶段 H | 已设计 |
| 后续直接用启动脚本 | start/stop/status/logs | 已设计 |
| 后续维护与升级 | update 的校验、备份、观察、回滚 | Shell/PowerShell 已实现，待真实升级 E2E |
| 安全卸载 | 默认保留 + `--purge` 双确认 | Shell/PowerShell 已实现，待 Windows/macOS E2E |
| 镜像公开发布 | ACR Personal Public 仓库已建立 | `0.1.0` 多架构清单已发布并固定 digest；匿名 token、manifest 与真实 blob 验证通过 |
| Windows 与 Mac 都能跑通 | 单合同 + 双平台实现与 E2E 矩阵 | 双平台脚本已实现；待真实 Windows/macOS 实机 E2E |

### 20.1 2026-08-17 实施快照

- 已实现 bot、codex、provider-proxy、mysql 四服务边界，以及 Shell/PowerShell 的 install、start、stop、status、logs、manage、update、uninstall。
- ACR `0.1.0` 总清单已发布，digest 为 `sha256:5269d0fdfb0c5c061e20cfa402d67fe4910e38c9d5b2fc43952eb912fe2b4e1e`，已回读包含 `linux/amd64` 与 `linux/arm64`。
- 新安装器与 `space.lock.json` 均按该总 digest 固定，不依赖 tag 漂移。
- Go build/vet/race test、Docker release contract、双架构非 root/Codex smoke 和本地 Secret canary 已通过。
- 该快照达到“公开验证版”：空 Docker 配置按总 digest 匿名拉取已通过；真实 Windows/macOS、飞书消息、百炼/DeepSeek、Workspace ZIP 导入和数据库恢复演练仍是稳定发行 Gate。

## 21. 实施前仍需人工确认的三点

1. 项目源码与公开镜像采用哪种 LICENSE。当前不得从“公开可拉取”推断成 MIT/Apache 等授权。
2. 课程集中安装如何控制并发与重试。个人版采用共享带宽且可能限流，应分批安装；若课程规模要求固定带宽，需要企业版或备用公开渠道。
3. XiaoPaw 导出侧是否同步升级为便携路径合同。若不升级，Bot 侧导入器需要承担更多 runtime projection；无论选择哪一侧，AIPM Dev/Use 真实导入 Gate 都不能省略。

## 参考资料

- [OpenAI Codex Configuration Reference](https://developers.openai.com/codex/config-reference/)
- [阿里云百炼：Codex 接入](https://help.aliyun.com/zh/model-studio/codex)
- [DeepSeek：使用 Responses API](https://api-docs.deepseek.com/zh-cn/guides/responses_api)
- [DeepSeek：接入 Codex](https://api-docs.deepseek.com/zh-cn/quick_start/agent_integrations/codex)
- [Docker Desktop for Mac](https://docs.docker.com/desktop/setup/install/mac-install/)
- [Docker Desktop for Windows](https://docs.docker.com/desktop/setup/install/windows-install/)
- [Docker multi-platform builds](https://docs.docker.com/build/building/multi-platform/)
- [阿里云 ACR：新个人版实例独立域名与共享带宽限制](https://help.aliyun.com/zh/acr/user-guide/individual-edition-instance-independent-domain-name-capacity-limit)
- [本轮 Sub Agent Review 收口报告](06-docker-deploy-review-report.md)
- 本仓库 `AGENTS.md`、`README.md`、`docker-compose.yml`、`config.yaml.template`、`cmd/server`、`cmd/appctl`、`internal/config`、`migrations/`。
