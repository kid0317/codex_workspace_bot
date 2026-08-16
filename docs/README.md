# 文档索引

本目录面向开源开发者和本机使用者，默认阅读顺序如下：

1. [需求分析](00-requirements-analysis.md)：说明项目要解决的问题、用户、边界和关键约束。
2. [产品需求文档 PRD](03-product-requirements.md)：说明功能范围、用户故事、验收口径和非目标。
3. [Codex App Server 协议调研](01-codex-appserver-protocol-research.md)：记录本项目依赖的 Codex App Server 协议事实。
4. [概要设计](02-redesign-high-level.md)：说明系统架构、核心实体、运行时、数据层、配置和可观测性。
5. [数据库建表与迁移](04-database-schema.md)：说明 `migrations/` 下的 MySQL 建表语句、自动迁移和恢复注意事项。
6. [Docker Deploy 跨平台安装器设计](05-docker-deploy-cross-platform-installer-design.md)：说明公开镜像、Space 模板、Shell/PowerShell 交互安装和 Workspace 导入合同。
7. [Story List](story/STORY_LIST.md)：按实际实现状态维护当前 Story 清单。
8. [Story 设计文档](story/)：每个 Story 的目标、范围、设计和验收标准。
9. [Story 交付 SOP](sop/story-design-to-delivery.md)：维护者继续开发 Story 时使用的流程约定。
10. [开源安全清单](open-source-readiness.md)：说明哪些文件可开源、哪些只能保留本地，以及提交前检查方法。

历史复盘、评审报告、过期设计和 agent 执行计划已移动到 [archive/](archive/)。这些文件用于追溯设计取舍，不是新读者理解项目的入口。
