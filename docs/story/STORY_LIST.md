# Story List

> 规则见 [Story 撰写规范](STORY_WRITING_SPEC.md)。
> 状态按当前代码、测试与已记录的真实验收事实维护；历史复盘与评审见 [归档目录](../archive/)。

| ID | Story | 状态 | 依赖 | 一句话目标 | 文档 |
|---|---|---|---|---|---|
| S01 | 基础框架、配置、MySQL 与飞书固定回显 | Delivered | Docker MySQL、测试飞书 App | 接收 text 并固定回显，同时保存一轮可追溯消息 | [S01](S01-基础框架与飞书接入-设计.md) |
| S02 | Worker 队列与流式卡片 | Delivered | S01、测试飞书 App interactive 权限 | 将 text 入按 p2p/group/App 隔离的 FIFO，按 Batch 回显未来 App Server 参数 | [S02](S02-Worker队列与流式卡片-设计.md) |
| S03 | 真实 Codex App Server 流式调试接入 | Delivered | S01、S02、已登录的本机 Codex CLI、测试飞书 App | 真实启动 bot-owned stdio child，完成一个 turn 并记录 raw、event 与 outcome 全量时间线 | [S03](S03-真实Codex-App-Server流式调试接入-设计.md) |
| S04 | 双区流式卡片展示 | Delivered（2026-07-13） | S03、测试飞书 App CardKit 权限、JSON 2.0 客户端 | work 双区流式卡与 companion final-only 多消息交付 | [S04](S04-双区流式卡片展示-设计.md) |
| S05 | 附件输入与飞书能力代理 | Delivered（2026-07-13） | S01–S03、测试飞书 App 资源/文件/docx 权限 | 接收并本地暂存图片/文件，并在当前会话安全代理发消息、上传文件/原生图片、创建及读取飞书文档 | [S05](S05-附件输入与飞书能力代理-设计.md) |
| S05.1 | 文档创建后转移发起人 Owner | 已实现，待 Delivered 审计（2026-07-13；p2p/group L4 已通过） | S05、Drive document permission、可信 sender identity | 每篇由 Bot 创建的 docx 转给触发该创建的消息发起人；失败不阻塞链接交付 | [S05.1](S05.1-文档创建后转移发起人Owner-设计.md) |
| S06 | 定时任务与 Agent 工具 | Delivered（2026-07-14；v4 list/update、Prompt、Script L4 已验证） | S01–S05、MySQL、已登录本机 Codex、测试飞书 App | Agent 管理自己在当前 App/频道/用户下的 CronTab 任务；Prompt 经 Worker FIFO 运行，Script 以 bot 当前 OS 用户直接执行并按静默策略交付 | [S06](S06-定时任务与Agent工具-设计.md) |
| S07 | Goal 持续执行与终态展示 | Delivered（2026-07-13；L3/L4 已验证） | S01–S05、现有命令实现、已登录本机 Codex、测试飞书 App | `/goal` 立即把目标作为首个 prompt 启动并持续展示，直到权威 Goal 终态 | [S07](S07-Goal持续执行与终态展示-设计.md) |
| S08 | Langfuse 全链路 Trace 可观测性 | In Development（2026-07-14；新 Project、P0 read-back、真实 realtime 明文 Trace 已验证；scheduled/故障注入仍待验收） | S03、S06、现有自托管 Langfuse、新 Project/Key | 以既有每请求 Trace ID 和按 Chat 聚合的 Session 追踪 Agent loop、工具、进展、明文业务 payload 与 Turn/loop/session usage | [S08](S08-Langfuse全链路Trace可观测性-设计.md) |
| S09 | 跨应用对话历史与 Langfuse 查询工具 | Draft / 待实现 | S01–S08、MySQL、当前自托管 Langfuse Project、测试飞书 App | 任意 App 可发现全部可查询 App/会话，并跨 App 读取完整 MySQL 对话历史和 Langfuse Trace | [S09](S09-跨应用对话历史与Langfuse查询工具-设计.md) |
| S10 | 飞书富文本列表识别与输出投递容错 | Draft / 待实现 | S01、S02、S04、S05、真实飞书富文本样本、CardKit 故障注入 | 将飞书聊天框里的列表/富文本消息转换为 plain text，并让 CardKit/发送失败不再误中断正常 Codex 请求 | [S10](S10-飞书富文本列表消息识别-设计.md) |
