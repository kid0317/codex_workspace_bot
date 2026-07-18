# Bot 控制脚本设计

**目标：** 在仓库根目录提供 `bot_controller.sh`，统一构建和管理本机 Codex Workspace Bot 进程。

## 命令契约

| 命令 | 行为 |
| --- | --- |
| `build` | 将 `./cmd/server` 编译为稳定路径 `runtime/codex_workspace_bot`，不启动或停止服务。 |
| `start` | 若 `codex-workspace-bot.service` 已活动，或发现本仓库任一 Bot 主进程，失败且不改变运行态；否则启动。 |
| `stop` | 先优雅停止服务，再仅清理本服务 cgroup 或停止前记录的 Bot/App Server 子进程；不会扫描或终止其他项目的 `codex app-server`。 |
| `restart` | 串行执行 `stop` 与 `start`。 |

## 进程与优雅退出

- 使用 `systemd-run --user` 创建固定单元 `codex-workspace-bot.service`，工作目录固定为仓库根目录，并从 `.env` 加载运行变量。
- 单元采用 `KillMode=mixed` 与 `TimeoutStopSec=45s`：停止时先向主进程发送 `SIGTERM`。主进程随后调用 `Manager.Shutdown(ctx)`：拒绝新消息、对每个活跃普通 Turn 走与 `/cancel` 相同的 `turn/interrupt` 路径，对 Goal 先确认 pause 再 interrupt，并等待中断完成；仅在此阶段结束或超时后关闭飞书连接、App Server client 和其余资源。仅当 45 秒后仍有残留，systemd 才对该 cgroup 强制清理。
- `stop` 对非 systemd 遗留实例仅匹配本仓库 `runtime/codex_workspace_bot` 或历史 `runtime/codex_workspace_bot_sNN` 的主进程，并在发信号前记录其后代 PID；它绝不按全局 `codex app-server` 名称杀进程。

## 启动与验证

- `start` 要求 `.env`、`config.yaml` 和稳定二进制均存在；运行 `systemd-run` 后等待 `/healthz=ok`，再等待 `/readyz` 中没有 `connecting`、`disconnected` 或 `failed` receiver。
- 启动失败时返回非零，并输出 unit journal 的末尾诊断；不打印 `.env` 值。
- `build` 只使用临时产物，成功后原子替换稳定二进制，避免失败构建破坏可运行旧产物。

## 验收

1. 不带参数或未知参数返回用法和非零状态。
2. `build` 产出可执行稳定二进制且不触发 systemd 命令。
3. `start` 在活动单元或识别到 Bot 主进程时失败；空闲时创建正确的 user unit。
4. `stop` 先请求 user unit 终止，且不会调用全局 `pkill codex`；超时后仅针对记录的 service PID/cgroup 做兜底。
5. 真实 `restart` 后，`healthz=ok`，所有 receiver 为 `connected`，主进程为新 PID。
6. 主进程收到 `SIGTERM` 时，活跃 Turn 必须有 `turn/interrupt` 协议请求；不得仅依赖 App Server 子进程被杀死。
