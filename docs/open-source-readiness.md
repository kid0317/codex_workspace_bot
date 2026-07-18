# 开源安全清单

## 1. 可以提交的文件

- Go 源码、测试、migration、脚本和 `docker-compose.yml`。
- `README.md`、`AGENTS.md`、`docs/` 下的当前设计文档和归档文档。
- `.env.example` 与 `config.yaml.template`，因为它们只包含占位符和环境变量名。
- `testdata` 中已经脱敏、用于自动化测试的 fixture。

## 2. 不能提交的文件

| 文件或目录 | 原因 |
|---|---|
| `.env` | 包含 MySQL 密码、附件/action key、Langfuse key。 |
| `config.yaml`、`config.*.yaml` | 可能包含本机路径、Langfuse 项目绑定、真实配置。 |
| `runtime/` | 包含 MySQL 数据目录、二进制、PID、运行态文件和数据库私钥。 |
| `logs/` | 包含本机运行日志、trace 和可能的业务标识。 |
| `.codex-workspace-bot/` | 默认附件缓存目录。 |
| `graphify-out/` | 本机生成的代码图谱缓存和查询记录。 |
| `task_plan.md`、`progress.md`、`findings.md` | 本机 agent 工作过程记录，不作为开源入口。 |
| `~/.codex/` 或任何 Codex 登录文件 | 包含账户认证状态。仓库内不应出现这类路径。 |

## 3. 提交前检查

建议每次公开推送前执行：

```bash
git status --short
git ls-files .env config.yaml runtime logs .codex-workspace-bot graphify-out
rg -n --hidden --glob '!.git/**' --glob '!runtime/**' --glob '!logs/**' --glob '!graphify-out/**' --glob '!*.db' --glob '!*.log' "(app_secret|encrypt_key|verification_token|secret_key|access_token|refresh_token|Authorization|Cookie|password:|MYSQL_ROOT_PASSWORD|LANGFUSE_SECRET_KEY|BEGIN (RSA|OPENSSH|PRIVATE)|sk-[A-Za-z0-9])" .
```

预期：

- `git ls-files ...` 不输出真实本地配置或运行数据。
- `rg` 只命中字段名、模板占位符、测试假值或说明文字。
- 如命中真实 key、token、私钥、密码或可用 DSN，必须从提交中移除并轮换对应凭据。

## 4. 模板文件

- `.env.example`：列出必须设置的环境变量，值必须保持占位符。
- `config.yaml.template`：列出服务配置结构，敏感值通过 `*_env` 引用。
- `docker-compose.yml`：只读取 `.env`，不硬编码密码。

真实启动时复制模板：

```bash
cp .env.example .env
cp config.yaml.template config.yaml
```

然后只编辑复制出的本地文件。
