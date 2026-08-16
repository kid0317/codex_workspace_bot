# Docker Deploy

本目录用于建设 Codex Workspace Bot 的跨平台 Docker 发行物：

- `install.sh`：macOS / Linux 交互安装入口。
- `install.ps1`：Windows PowerShell 交互安装入口。
- Bot 多架构镜像构建文件。
- Compose、Space、App、Provider 与日常管理脚本模板。
- ACR 发布与跨平台安装验收脚本。

当前状态：**v0.1 实现中。** 四服务 Secret 隔离、双平台安装入口和启动、停止、状态、日志、App 管理、更新、卸载脚本已经落地；在公开镜像完成双架构构建、匿名拉取与真实飞书 E2E 前，不把它标记为稳定发行版。

## 目录

- `image/`：固定 Codex 版本的非 root 多阶段镜像。
- `templates/`：安装后 Space 使用的 Compose、配置和生命周期脚本。
- `release/`：带 SHA-256 的发行清单；运行时按 digest 固定镜像。
- `install.sh` / `install.ps1`：macOS/Linux 与 Windows 交互安装入口。
- `publish.sh`：维护者多架构构建与 ACR 发布入口，不包含任何阿里云凭据。
- `tests/`：发行结构和 Secret 边界检查。

公开镜像仓库：

```text
crpi-0c1kby082wk3ovcx.cn-hangzhou.personal.cr.aliyuncs.com/
  codex-workspace/codex-workspace-bot
```

ACR 个人版用于课程和个人体验，不承诺生产 SLA。发布脚本只从当前 Git `HEAD` 生成临时构建上下文，因此不会把工作树中的 `.env`、密钥或未提交文件带进镜像。

## 开发验证

```bash
bash docker-deploy/tests/test_release_contract.sh
go test ./...
```

维护者发布：

```bash
docker login <ACR域名> --password-stdin
./docker-deploy/publish.sh 0.1.0
```

登录凭据只进入进程和临时 Docker 配置，禁止写进仓库、脚本、构建参数或发行包。

实现前请先阅读：

- [Docker Deploy：跨平台交互安装器与 Space 发行设计](../docs/05-docker-deploy-cross-platform-installer-design.md)
- [概要设计](../docs/02-redesign-high-level.md)
- [Codex App Server 协议调研](../docs/01-codex-appserver-protocol-research.md)

正式实现必须遵守设计文档中的双平台单合同、Secret 不进 Git、Responses Provider、Space 事务生成与真实 E2E Gate。
