# SOP：本地 Story 从设计到 Delivered

## 目的与触发

当新建或重大改造一个本地 Story 时使用。目标是把“设计正确、代码可测、运行已应用、人工边界已验证、文档可恢复”串成一条闭环，而不是把代码通过测试误当成交付。

## 0. 开始前

1. 读取 `AGENTS.md`、HLD、Story List、相关 Story、现有 review、README 和运行规则。
2. 明确本 Story 的目标、非目标、外部依赖、人工决策、验收场景和最终人工集成校验。
3. 将全局架构和当前 Story 分开：HLD 描述未来全局，Story 只描述本期切片。
4. 若要复用旧系统，先用 `rg` 读旧实现和配置；提取行为，不复制已被废弃的边界。

## 1. 设计与评审

1. 在 `docs/story/Sxx-*.md` 写目标、范围、数据契约、包边界、验收、DoD、人工验证。
2. 所有 Story 设计/评审/复盘写入 `docs/story/`；跨 Story 可复用操作手册写入 `docs/sop/`。
3. 对外部协议（飞书、App Server、Langfuse）先调研官方/本地证据，再落字段和超时。
4. 对高风险设计发起独立评审：技术架构、质量/安全、产品架构。汇总报告必须区分 Sxx 阻塞、重要项和 Future。
5. 用户裁决后立即回写 HLD、Story 与 Story List；不要把已裁决事项留在评审报告中。

## 2. 实现：TDD 与小批次

对每一个行为执行 RED → GREEN → REFACTOR：先写最小失败测试，确认失败原因正确，再写最小实现，最后运行目标包及相关全量测试。严禁先写实现再补“证明性”测试。

建议批次：配置/迁移 → 存储事务 → Router → 外部 SDK 适配 → CLI → 日志/运行时 → 文档。每批次至少执行 `gofmt`、目标 `go test`；涉及并发则执行 `go test -race`。

## 3. 配置、数据与运行态应用（硬门禁）

每次改动运行时代码、配置、迁移、初始化/导入数据或依赖服务时，按顺序执行：

1. 更新并校验配置模板/环境变量/迁移/初始化数据。
2. 运行相关测试和 `go vet ./...`。
3. 使用 `./bot_controller.sh build` 构建本服务；不得手写 `go build` 后直接运行二进制作为运行时验证路径。
4. 应用迁移与数据导入。
5. 已有实例使用 `./bot_controller.sh restart`，未运行时使用 `./bot_controller.sh start`；不得使用 shell 后台命令、`nohup`、手写 `systemd-run` 或直接执行 runtime 二进制。
6. 记录脚本启动的新实例时间/版本，检查 `/healthz` 和（若有）`/readyz`。脚本停止时必须让 Bot 主进程对在途 Turn 执行 `/cancel`/`turn/interrupt`，再关闭其专属 App Server；不得全局杀死其他 Codex App Server。
7. 用数据库查询确认 migration 版本、启用配置和关键状态。
8. 只有上述全部成立，才能请用户验证；绝不让用户验证旧进程。

## 4. 外部边界与人工验收

自动 fake 只能证明本地契约。涉及真实外部边界时，执行 Story 的“最终本地集成校验”：

- 发送唯一标识的真实输入；
- 保存响应、Trace ID、日志事件、数据库状态和外部服务 health；
- 对多 App/多租户系统验证隔离键；
- 对持久化服务验证重启后的数据；
- 检查日志不含 Secret、密码、认证头和用户正文。

## 5. Delivered 审计

在标记 Delivered 前，逐条对照 Story DoD。每条必须有当前证据：代码/测试、运行输出、数据库、日志或人工验证。证据不足即保持 `In Development` 或 `Ready for final local validation`。完成后同步 Story 状态、Story List、README、HLD（若契约改变）和交付证据。

## 6. 每次 Delivered 后的强制复盘

1. 写 `docs/archive/story-retrospectives/Sxx-全过程复盘-YYYY-MM-DD.md`。
2. 从会话记录、diff、测试、运行日志、DB、评审和用户纠偏还原时间线。
3. 列出工具/运行操作、反复、踩坑、根因和残余风险；不写 Secret/正文。
4. 分类每个问题：缺知识、缺 SOP、缺项目规则、缺工具、缺自动门禁或过期上下文。
5. 更新本 SOP 或专门 SOP；在 `AGENTS.md` 加索引；删除过期/重复指引。

## 7. S01 学到的不可违反规则

- “持续推进到 Delivered”是终止条件，不是进展说明；不要因局部完成结束工作。
- 运行验证必须绑定新进程、新配置、新迁移和新数据。
- 评审发现的交付阻塞项必须进入可验证整改清单，不能只写报告。
- 日志切分不等于磁盘安全：必须定义留存/上限和失败可见性。
- 同群多 App 的正确性必须由真实 `app_id + chat_id` 数据证据证明。

## 8. S02 学到的不可违反规则

- 运行时代码重启后，只有**新进程启动时间之后**产生的唯一 trace/message 才能作为该版本的真实验收；旧进程已接管的内存消息不补放也不用于判定新版本。
- 飞书 interactive message 的内容更新必须用 Message PATCH；Message Update/PUT 不能替代卡片 PATCH。外部卡片验收必须保存创建和更新的同一 message ID、最终用户可见结果以及数据库终态。
- Worker 的“空闲”只能由显式状态机判断：`InProcess` 绝不能因没有新消息而回收。timeout 必须经历 `Stopping`、cancel、grace、active/pending 用户可见收尾、Worker 移除和 goroutine 退出；work 与 companion 不能拥有不同的超时语义。
- card create/update、队列/worker 拒绝和 timeout 都必须给用户确定性文本兜底；每种兜底都要在 MySQL 走合法的条件状态转移，不能只发送文本或只写日志。
- 任何独立实现评审的 blocker 在修复后必须重新复审；没有 blocker-only 复审通过，不得将 Story List 或 Story 状态改为 Delivered。

## 9. S03 学到的不可违反规则

- 对运行时指标，计时边界必须由其权威协议事件定义并在该边界处捕获。例如 Turn 时长从 `turn/start` 被 App Server 接受后开始，到同一 Turn 的权威终态结束；不得混入 Thread 恢复、发卡或飞书 PATCH。
- 一次真实外部样本发现 JSONL 索引缺口后，修复必须用**新进程的新 trace**重验字段存在性、`seq` 连续性和跨文件 join，不能用旧 raw 文件或仅单元测试宣称闭环。

## 10. S04 学到的不可违反规则

- 当真实 SDK/L4 否定设计中的组件级 CardKit 更新而全量 entity update 已验证时，立即把全量更新回写为正式主路径；PATCH 只能是单次更新降级，不能因一次失败永久接管后续卡片。
- companion 的“成功终态”并不等于可安全释放 Worker：TerminalArbiter 必须固定首个终态原因，DeliverySlot 必须覆盖 marker、发布、取消和 done；控制面只能在 slot done 后释放同 channel。
- workflow JSONL 是 companion 逐段交付的本机权威证据。写入必须可返回错误；写失败时停止后续段并以稳定错误码收尾，不能把 `slog.Info` 当成可靠写入。
- 用户明确推迟无法进行的真实外部失败验证时，应在 Story/DoD 中记录推迟边界，保留 focused unit tests，不得把它伪装成已经验证的 L4。

## 11. S05 学到的不可违反规则

- 同一个“上传并发送”动作若会按内容分流到不同的外部 API（例如原生图片与普通文件），action ledger 的 `succeeded` 只能证明调用成功，不能替代对用户可见消息类型的 L4 验证；必须为每种呈现分支保留明确证据。
- 运行时依赖的加密 key 不能只存在于一次性的父进程环境；重启前必须确认稳定本机配置/secret 已就绪，并记录旧密文是否仍可恢复。
- 用户明确允许使用本次实现期内的历史真实成功记录时，可以将其纳入 Delivered 证据；需保留时间、外部边界和脱敏终态，且不得继续保留“尚未验证”的相反文档结论。

## 12. S06 学到的不可违反规则

- dynamic tool 的 `list → update` 链路必须把 list 返回的标识字段直接作为 update Schema 的标识字段，并用真实返回对象或等价回归测试覆盖；不得要求 Agent 猜测另一个内部字段名。
- 任一 dynamic-tool Schema 改动必须递增持久 catalog version，并以 archive/start 取得新 Thread 后再把该 Thread 当成验收对象；只重启 bot 不能更新已 resume Thread 的工具描述。
- 异步 worker/executor 的 run 状态必须先持久化为消费者允许的状态，再发布给可能立即开始的消费者；测试应在发布调用内同步启动消费者以覆盖该竞态。
