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
- 复用私有编译的 `appctl list/create --config ./config.yaml` 访问 MySQL；不直接执行 SQL。
- `create` 前读取不含 Secret 的 TSV 列表，分别检查名称和飞书 App ID，以便尽早给出人话提示；最终防竞态保证来自数据库原子 INSERT-only，而不是这次 preflight。
- `appctl create` 必须使用数据库原子 INSERT-only；即使另一个管理员恰好在 list 与 create 之间登记同名或同 App ID，create 也失败且不覆盖。`update` 只更新已存在名称；首次安装若需幂等行为显式使用 `upsert`。
- 脚本在询问 Secret 前把 `appctl`、`safedotenv` 和 `receivercheck` 编译到权限为 `0700` 的临时私有目录；Secret 只经 stdin 和 `--secret-stdin` 交给最终二进制，不进入参数、环境变量或临时文件，调用结束后立刻清理变量与私有二进制。
- 入口先关闭 xtrace；`.env` 只能由共享的纯数据 loader 解析，绝不 `source` 或 `eval`。loader 只接受明确允许的键和 setup 的 `printf %q` 字面转义，拒绝 `set -x`、函数、trap、命令替换、危险 shell 键和重复键，并只给 appctl/controller/runtime 生成最小子进程环境。
- Workspace 必须是存在的绝对目录，且不得含 tab、回车等会破坏 TSV 回读的控制字符；可信 OS Home 在加载 dotenv 前捕获，根目录、系统目录、Home 密钥目录、`CODEX_HOME`、`USER_DIR`、`AIPM_MOUNT_USER_DIR` 等动态敏感目录及其过宽祖先都被拒绝。
- 模型和 effort 默认继承现有 `$CODEX_HOME/config.toml`，读不到时回退为 `gpt-5.6-terra/high`。
- 交互入口面向“新增后立刻可用”的场景，因此固定创建 enabled App；需要禁用时使用显式 `appctl disable` 管理命令。
- `create` 成功后再次执行 `list`，精确回读名称、App ID、Workspace 和 enabled 状态；只有回读一致才调用 `./macos_bot_controller.sh restart` 刷新 receiver，不直接启动进程。
- controller 返回成功后仍要做严格激活回读：`/healthz=ok`，`/readyz` 的内部 receiver ID 集合必须与 enabled App 的内部数据库 ID 集合完全相同，且每个 state 都严格为 `connected`。`connecting`、`reconnecting`、`failed`、空列表、缺少 ID 或同数量的错误 ID 都继续等待，超时则判定未生效。
- readiness 使用 Go JSON 解析器只检查顶层 `receivers` 对象，其他位置恰好出现的 `state` 不参与判断；健康地址从 `config.yaml` 的 `server.listen_addr` 读取，只允许 `127.0.0.0/8`、`localhost`、`::1` 等 loopback 和合法端口。add 脚本与 controller 的每次 curl 都设置连接与总时限。

## 场景验收

- 单个配置、连续两个配置均成功调用相应次数的 `create` 和 `restart`。
- 用户取消、非法名称、相对或不存在的 Workspace、非法 App ID 均不调用 `create`。
- 重复名称或 App ID 均在 preflight 阶段失败，且不调用 `create`。
- App Secret 不出现在标准输出、标准错误、fake 命令日志、`ps` 命令行快照或测试工作目录普通文件中。
- 现有 `.env` 中的 `set -x`、函数、DEBUG trap、命令替换和 `PATH/HOME/BASH_ENV` 等危险键会在任何交互或数据库动作前被拒绝；成功与失败路径均扫描 DB/provider/crypto sentinel，stdin 仍拒绝空值、换行和超过 256 bytes 的值。
- 空模型/effort 输入会继承已初始化的 Codex runtime 配置；控制字符路径在写入前失败。
- `appctl list/create` 失败时退出非零；`create` 失败不重启。
- `create` 返回成功但回读不一致时不重启、不声称生效，并提示“登记状态需人工核对”。
- `restart` 失败时退出非零，并打印“已登记但未生效”和 `./macos_bot_controller.sh restart`。
- 严格激活覆盖 `reconnecting -> connected`、receiver 数量不足后补齐、同数量错误 ID，以及最终超时的失败提示；非默认 loopback 端口也有自动测试。

## 外部验收边界

本地自动测试使用 fake `go`/controller/MySQL、真实 `safedotenv` helper、临时 `.env/config.yaml` 和临时 Workspace，不连接真实飞书或真实 MySQL。真实 macOS launchd、Homebrew MySQL 8.4、两套独立飞书企业自建应用的长连接与消息分别路由到两个 Workspace，仍是独立 E2E Gate。
