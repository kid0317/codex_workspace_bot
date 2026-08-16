# Docker Deploy Sub Agent Review 收口报告

日期：2026-08-16
范围：`docker-deploy/`、Codex App Server 运行边界、Secret、更新/卸载与 ACR 发行
结论：**核心设计阻断已修复，多架构镜像已公开并通过匿名拉取链路验证；当前可进入公开验证，但尚不能标记为面向学员的稳定发行版。**

## 1. Review 发现与处理

| 级别 | 发现 | 处理结果 |
|---|---|---|
| Critical | Bot 把 Provider、飞书、DB、HMAC Secret 注入同一环境，`danger-full-access` Agent 可读取 | 已拆为 bot、codex、provider-proxy、mysql 四服务；Codex 不加载 `.secrets`，Bot child env 使用 allowlist |
| High | App Server 与 Bot 放在同一进程边界 | 已实现 `codex-remote` + byte-transparent `codex-bridge`，独立 Codex 容器只运行唯一 App Server |
| High | Provider Key 若直接给 Codex，仍可被 Shell 读取 | 已实现固定上游 `provider-proxy`；Codex 只持有无价值占位 token，proxy 删除来访认证头后注入真实 Key |
| High | 飞书 Secret 可能进入 argv、进程列表或日志 | 已实现 `secure-appctl --secret-file`；安装器使用临时只读文件挂载，结束后删除 |
| High | Docker 构建上下文可能带入 Secret | 已使用 `Dockerfile.dockerignore`、显式 `COPY` 和 Git `HEAD` 临时构建上下文；禁止 `COPY . .` |
| P0 | 缺少 update / uninstall 一等生命周期入口 | Shell 与 PowerShell 均已提供 start、stop、status、logs、manage、update、uninstall |
| P0 | 更新只切旧镜像不能覆盖不可逆数据库 migration | update 前强制导出 MySQL dump，发行清单声明 DB rollback 策略；失败恢复旧配置与旧镜像，数据恢复仍按兼容声明执行 |
| P1 | 容器监听、停止宽限与宿主机暴露不明确 | Bot 监听 `0.0.0.0:8080`，宿主机只绑定 `127.0.0.1`；Bot/Codex `stop_grace_period` 为 45 秒 |
| P1 | purge 可能误删另一 Space | 默认 uninstall 只 down 并保留；purge 要输入精确 Space ID，只删除当前 Compose volume 与明确列出的受管路径，不使用 prune |
| P1 | Windows Secret ACL 与 Workspace 拷贝语义不足 | 安装器将 Secret ACL 收紧到当前用户；修复 PowerShell `LiteralPath` 与通配符冲突，并补覆盖确认 |
| P1 | 单次双架构长 push 容易在 ACR 临时令牌到期时失败 | 发布器改为分别发布 amd64/arm64，每步可刷新临时登录，最后单独创建总 manifest |

## 2. 已通过的本地 Gate

- `go build ./...`
- `go vet ./...`
- `go test ./... -race -count=1`
- Docker release contract 与 Shell 语法检查
- Compose 四服务结构、loopback 端口、45 秒停止宽限、Codex 无 Secret env_file 静态检查
- amd64、arm64 镜像构建
- 两个架构均为非 root UID/GID 10001
- 两个架构均包含 `codex-cli 0.147.0`，且 `codex app-server --help` 可运行
- 镜像 Config/History 未发现 AccessKey、API Key、App Secret 或密码模式
- 按真实 Codex 容器 env/mount 合同运行本地 Secret canary：环境与 `/proc/self/environ` 均不含飞书、MySQL、业务 DB、Provider 或 ACR Secret，且 `/space/.secrets` 不存在
- 无凭据匿名 pull token、总 manifest 与真实 ARM blob HEAD 均返回 200，blob 长度和 digest header 匹配发布清单；全新空 `DOCKER_CONFIG` 按总 digest 执行 `docker pull` 成功
- 飞书设计文档创建后完成标题、正文、关键安全章节与末尾参考资料回读

## 3. 仍未关闭的稳定发行 Gate

1. **真实双平台 E2E**：本轮环境没有真实 Windows Docker Desktop 与 macOS Docker Desktop，PowerShell 脚本尚未在 Windows 执行。
2. **真实飞书 Secret canary**：本地容器边界 canary 已通过；仍需从真实飞书消息触发 Agent，验证工具调用、Workspace 与完整运行链路均看不到 Bot、Provider、DB、HMAC 和其他 App Secret。
3. **真实 Provider E2E**：百炼 Responses 与 DeepSeek Responses 均需使用课程实际 Key 完成一次 App Server 调用；本轮未把任何 Key 写入测试或仓库。
4. **Workspace 导入范围**：当前 `manage` MVP 支持目录复制；设计中的 ZIP 防穿越、symlink/hardlink、zip bomb 与 XiaoPaw Dev/Use runtime projection 尚未实现。
5. **数据库恢复演练**：已生成升级前 dump，但需要用包含真实 migration 的 N-1 → N 故障注入完成一次自动恢复演练。
6. **供应链签名**：v0.1 有 SHA-256 清单与 digest 固定，但签名、完整 SBOM/provenance、漏洞门禁和 immutable tag 仍需补齐。
7. **许可证**：仓库尚无 LICENSE；镜像只能标 `NOASSERTION`，公开拉取不等于授予源码许可证。

## 4. ACR 发行结果（2026-08-17）

- 仓库：`codex-workspace/codex-workspace-bot`，个人版属性为 Public；无凭据匿名 token、manifest 与真实 blob 验证通过。
- AMD64 与 ARM64 标签均完成鉴权回读；`0.1.0` 总清单同时包含 `linux/amd64` 与 `linux/arm64`。
- 总 digest：`sha256:5269d0fdfb0c5c061e20cfa402d67fe4910e38c9d5b2fc43952eb912fe2b4e1e`。
- 安装器和新 Space lock 已固定到总 digest；不使用 `latest`，也不只按可变版本 tag 启动。
- 本结论是“公开镜像发行成功”；稳定课程发行仍取决于真实双平台、飞书、Provider、导入和恢复 Gate。

## 5. 发布裁决

- 可以做：匿名拉取、架构/启动/配置联调、Windows 与 macOS 实机验收。
- 不可以声称：稳定课程发行版、生产级部署方案、已完成真实飞书/Provider/回滚 E2E。
- 进入稳定发行前，至少关闭上节第 1～4 项，并由课程维护者决定集中安装的带宽/重试方案与 LICENSE。
