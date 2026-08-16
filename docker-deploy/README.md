# Docker Deploy

本目录用于建设 Codex Workspace Bot 的跨平台 Docker 发行物：

- `install.sh`：macOS / Linux 交互安装入口。
- `install.ps1`：Windows PowerShell 交互安装入口。
- Bot 多架构镜像构建文件。
- Compose、Space、App、Provider 与日常管理脚本模板。
- ACR 发布与跨平台安装验收脚本。

当前状态：**仅建立目录与设计入口，安装脚本、镜像和模板尚未实现，不能用于真实安装。**

实现前请先阅读：

- [Docker Deploy：跨平台交互安装器与 Space 发行设计](../docs/05-docker-deploy-cross-platform-installer-design.md)
- [概要设计](../docs/02-redesign-high-level.md)
- [Codex App Server 协议调研](../docs/01-codex-appserver-protocol-research.md)

正式实现必须遵守设计文档中的双平台单合同、Secret 不进 Git、Responses Provider、Space 事务生成与真实 E2E Gate。
