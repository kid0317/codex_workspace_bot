# macOS 原生安装：交互式添加 Workspace

## 用户场景

1. 作为已经完成 macOS 原生初始化的用户，我希望运行一个交互脚本，给现有 Bot 增加一个飞书 App 和一个绝对路径的 Workspace，并在登记成功后立即让 receiver 生效。
2. 作为管理多个 Workspace 的用户，我希望一次连续添加两个或更多配置，不必反复启动脚本。
3. 作为误输信息的用户，我希望能在最终确认时取消，并且数据库和 Bot 进程都不发生变化。
4. 作为已有配置的用户，我希望重复 Bot 名称或重复飞书 App ID 在写数据库前被拒绝，绝不覆盖旧配置。
5. 作为凭据持有者，我希望 App Secret 隐藏输入，且不出现在命令参数、日志、进程命令行或临时文件中。
6. 作为运维者，我希望数据库登记失败时不重启；登记成功但重启失败时，脚本明确说明“已登记但未生效”，并给出恢复命令，而不是声称数据库已回滚。

## 范围与接口

- 入口只服务已完成 `scripts/macos_native_setup.sh` 的安装，不负责安装依赖、创建 MySQL 或首次启动。
- 复用 `go run ./cmd/appctl list/create --config ./config.yaml` 访问 MySQL；不直接执行 SQL。
- `create` 前读取不含 Secret 的 TSV 列表，分别检查名称和飞书 App ID。当前 `create` 是 upsert，因此这一步是防止覆盖已有 App 的必要关卡。
- App Secret 只通过临时环境变量和 `--secret-env` 传给 `appctl`，函数返回前清理。
- 入口先关闭 xtrace；Workspace 必须是存在的绝对目录，且不得含 tab、回车等会破坏 TSV 回读的控制字符。
- 模型和 effort 默认继承现有 `$CODEX_HOME/config.toml`，读不到时回退为 `gpt-5.6-terra/high`。
- 新增 App 固定为启用状态；当前 `appctl` 的 upsert 不支持可靠地创建 disabled App，因此不提供会误导用户的禁用选项。
- `create` 成功后再次执行 `list`，精确回读名称、App ID、Workspace 和 enabled 状态；只有回读一致才调用 `./macos_bot_controller.sh restart` 刷新 receiver，不直接启动进程。
- controller 返回成功后仍要做严格激活回读：`/healthz=ok`，`/readyz` 的 receiver 数等于 enabled App 数，且每个 state 都严格为 `connected`。`connecting`、`reconnecting`、`failed`、空列表或数量不足都继续等待，超时则判定未生效。

## 场景验收

- 单个配置、连续两个配置均成功调用相应次数的 `create` 和 `restart`。
- 用户取消、非法名称、相对或不存在的 Workspace、非法 App ID 均不调用 `create`。
- 重复名称或 App ID 均在 preflight 阶段失败，且不调用 `create`。
- App Secret 不出现在标准输出、标准错误、fake 命令日志、`ps` 命令行快照或测试工作目录普通文件中。
- 空模型/effort 输入会继承已初始化的 Codex runtime 配置；控制字符路径在写入前失败。
- `appctl list/create` 失败时退出非零；`create` 失败不重启。
- `create` 返回成功但回读不一致时不重启、不声称生效，并提示“登记状态需人工核对”。
- `restart` 失败时退出非零，并打印“已登记但未生效”和 `./macos_bot_controller.sh restart`。
- 严格激活覆盖 `reconnecting -> connected`、receiver 数量不足后补齐，以及最终超时的失败提示。

## 外部验收边界

本地自动测试只使用 fake `go`、fake controller、临时 `.env/config.yaml` 和临时 Workspace。真实 macOS、Homebrew MySQL、飞书企业自建应用长连接与消息路由仍是独立 E2E Gate。
