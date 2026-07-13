# Story List

> 规则见 [Story 撰写规范](STORY_WRITING_SPEC.md)。

| ID | Story | 状态 | 依赖 | 一句话目标 | 文档 |
|---|---|---|---|---|---|
| S01 | 基础框架、配置、MySQL 与飞书固定回显 | Delivered | Docker MySQL、测试飞书 App | 接收 text 并固定回显，同时保存一轮可追溯消息 | [S01](S01-基础框架与飞书接入-设计.md) |
| S02 | Worker 队列与流式卡片 | Delivered | S01、测试飞书 App interactive 权限 | 将 text 入按 p2p/group/App 隔离的 FIFO，按 Batch 回显未来 App Server 参数 | [S02](S02-Worker队列与流式卡片-设计.md) |
| S03 | 真实 Codex App Server 流式调试接入 | Delivered | S01、S02、已登录的本机 Codex CLI、测试飞书 App | 真实启动 bot-owned stdio child，完成一个 turn 并记录 raw、event 与 outcome 全量时间线 | [S03](S03-真实Codex-App-Server流式调试接入-设计.md) |
| S04 | 双区流式卡片展示 | Delivered（2026-07-13） | S03、测试飞书 App CardKit 权限、JSON 2.0 客户端 | work 双区流式卡与 companion final-only 多消息交付 | [S04](S04-双区流式卡片展示-设计.md) |
| S05 | 附件输入与飞书能力代理 | Delivered（2026-07-13） | S01–S03、测试飞书 App 资源/文件/docx 权限 | 接收并本地暂存图片/文件，并在当前会话安全代理发消息、上传文件/原生图片、创建及读取飞书文档 | [S05](S05-附件输入与飞书能力代理-设计.md) |
| S06 | 飞书斜杠命令与时间上下文 | In Development（2026-07-13） | S01–S04、已登录本机 Codex、测试飞书 App | 用 `/new`、`/cancel`、`/status`、`/goal`、`/help` 管理频道会话，并向普通文本传递上海时区时间 | [S06](S06-飞书斜杠命令与时间上下文-设计.md) |
