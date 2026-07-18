# S04 独立评审：与 CC Workspace Bot companion 输出语义对比

> **评审类型**：只读设计评审（CC Workspace Bot 兼容性 / 迁移风险）
> **评审日期**：2026-07-12
> **范围**：S04 的 work/companion 输出设计，重点检查 companion final-only、多消息、`[[SEND]]`、过滤、延迟、错误/取消和 MySQL 交付表达。
> **结论**：**有条件通过（1 个 S04-P1 设计缺口需修复；1 个产品迁移决策需用户确认）**。S04 对旧 bot 的分段与逐段发送主语义覆盖较完整，但“飞书已送达、随后 MySQL 收尾失败”的幂等边界尚未定义；旧 companion 的 persona 输出过滤不应被误认为已随 S04 迁移。

## 1. 审查范围、方法与证据

本报告未改动 S04、HLD、运行时代码或旧 bot。以当前工作树中的 S04/HLD 和 `/root/cc_workspace_bot` 当前源码为证据，逐项对照：

| 主题 | S04 / HLD 证据 | CC Workspace Bot 当前实现 / 测试证据 |
|---|---|---|
| 模式分流 | `docs/story/S04-双区流式卡片展示-设计.md:23-35, 55-57, 119-131`；`docs/02-redesign-high-level.md:190-196, 373-391` | `internal/session/worker.go:198-205, 309-332`；`internal/config/config.go:27-42` |
| 分段与 fallback | S04:123-129, 133-141 | `internal/session/segment.go:36-83, 105-224`；`internal/session/segment_test.go:11-220` |
| 多消息延迟、错误、取消 | S04:127-129 | `internal/session/segment.go:235-291`；`internal/session/worker.go:334-379`；`internal/session/worker_test.go:166-388` |
| companion 最终内容过滤 | S04:121-141（presentation sanitizer + lexer） | `internal/session/filter.go:12-118`；`docs/companion-output-filter-design.md:48-63, 263-309` |
| 持久化与交付状态 | S04:129-131, 181-186；HLD:584-607 | `internal/session/worker.go:282-307, 399-412`；`internal/model/models.go:30-39` |

旧 bot 的 `internal/session` 测试无法在本机重跑：该仓库 `go.mod` 要求 Go ≥ 1.24，而当前 shell 是 Go 1.23（`go test ./internal/session` 在编译前退出）。以上测试覆盖范围来自当前源码和测试文件的静态阅读，不能替代一次可执行复验。

## 2. 兼容性结论与刻意迁移差异

### 2.1 已保持或明确增强的旧行为

| 行为 | 结论 | 说明 |
|---|---|---|
| companion 无占位卡、只发文本 | **一致** | 旧 bot 仅在非 companion 时 `SendThinking`；S04 将此收紧为 Turn 终态前零飞书出站。后者更符合本 Story 的 final-only 产品要求。 |
| 精确 `[[SEND]]` 分段 | **一致** | 旧 `SplitSegments` 保序、trim、丢弃空段；S04 明确复用该语义并要求 marker 不出站。 |
| 无 marker 的 fallback | **一致** | 双换行 → 超 80 rune 才按句末 → 短段合并/硬切；若 fallback 超 3 段则保留原文单段。S04:126-127 与旧 `segment.go:64-83` 对齐。 |
| 打字节奏 | **一致** | 旧实现在第一段发送前也等待 300–1500ms，后续 600–2000ms，包含基于上段阅读/下段输入的 jitter。S04:127-128 明确保留该基线，不应误实现为固定 400ms。 |
| 限流、普通错误与取消 | **一致** | 旧实现为当前段 500ms 后单次重试；失败仍继续后续段；context 取消立刻停止未发送段。S04:128 已逐项要求。 |
| 送达状态可观察性 | **增强** | 旧 bot 仅在发送前写 assistant 文本，且不保存每段发送结果；S04 定义首成功 ID、partial/none error code 与逐段 trace，能避免把局部送达伪装为全送达。 |
| 非规范 marker 的兼容 | **刻意增强** | 旧实现仅精确 `strings.Split("[[SEND]]")`。S04 Lexer 支持大小写/空格/全角双括号及受限的独立行单括号，且保护 code/转义文本。这是为模型不稳定输出新增的产品能力，不是旧行为回归。 |

### 2.2 不是缺陷、但必须显式承认的产品/架构差异

1. **终态时机改变**：旧 bot 取得 CLI 的完整 `ExecuteResult` 后才开始分段；S04 等 App Server 的权威 `turn/completed=completed` 后才发送。这是 Codex App Server 的正确适配，也是更强的“不展示半成品”保证，不应倒退为旧时机。
2. **持久化层改变**：旧 bot 用 SQLite 的 `Message{Content, FeishuMsgID}`，并在发送前持久化；S04 用 MySQL 单轮 `messages` 与条件状态迁移。这是产品架构改造，不是必须复制的实现。
3. **段宽与字节上限不可混同**：旧的 80 rune 是体验目标；S04 work-mode 的 140 KiB 是飞书请求安全预算。companion 必须仍遵守旧 80-rune/fallback 语义，不能因为 text API 上限更大而改成 140 KiB 一段。

## 3. 发现、风险与建议

| 编号 | 级别 | 发现与精确证据 | 风险 | 建议 | 需用户裁定 |
|---|---|---|---|---|---|
| **S04-CC-P1** | **P1（实现前应补）** | S04 在成功 Terminal 后先发送全部段，再调用 `CompleteBatch` 写首成功 ID/error code（S04:128-131、157、186），但未规定 **飞书已成功发送而 MySQL 条件更新/事务失败** 时的状态和重试所有权。新设计又不新增 segment 明细表；普通 `SendText` 没有可供恢复去重的 per-segment durable id。 | 若实现将整个收尾重试，已发段可能重复；若不重试，Message 可能长期处于 in-progress/缺失交付摘要，无法诚实解释用户已收到的内容。 | 在 S04 补一条 delivery/DB 失败状态机：飞书调用一旦得到成功响应即写入 attempt-local immutable delivery ledger；`CompleteBatch` 失败只能重试**数据库条件更新**，绝不重发段；最终 DB 失败时按既有确定性失败/告警策略标记为 storage-finalize failure，并保留 trace。服务重启仍不重放 S03 中断 Turn。为此写 L2：首段成功→DB 首次失败→DB 重试成功；以及 DB 永久失败时确认无重复 SendText。 | 否（安全的默认是“绝不为补 DB 而重发可见段”）。 |
| **S04-CC-P2** | **P2（产品迁移差异）** | 旧 bot 在发送和持久化前会用 Stop hook 的 `FINAL_REPLY.md` 替换原始 CLI 文本，文件缺失则 fail-open，并有 canary（`filter.go:12-118`；设计文档:263-309）。S04 只有 presentation sanitizer 与 delimiter lexer（S04:121-141），没有等价的 persona/叙事泄露过滤或健康检查。 | 若旧 companion workspace 依赖该过滤协议，迁移到 Codex 后可能重新露出原本被滤掉的操作性台词；反过来，贸然复制 Claude hook 也违反“Codex 原生、不做 Claude 兼容”的方向。 | 明确将旧 `FINAL_REPLY.md` 协议列为“不迁移”的产品决定；若仍需相同的角色输出质量，单开 Codex-native output policy Story，以 App Server final content 为输入、定义可评测规则/告警，而不是复用 Claude hook。 | **是**：确认当前 companion 是否需要保留旧 persona 输出过滤的产品效果。 |
| **S04-CC-P3** | **P2（配置契约不完整）** | HLD 配置仅有 `streaming.companion_segment_delay_ms: 400`（HLD:661-667）；但旧的真实节奏由 BaseDelay、读/打字速度、首段/后续上下限、jitter 共八项控制（`segment.go:12-34, 235-291`）。S04:127 将它们称为体验基线，但未说明配置字段只映射 BaseDelay，还是所有值固定。 | 实现者可能把 `companion_segment_delay_ms` 误作固定段间等待，破坏首段/长短文本差异；也可能增加一组无依据配置，扩大运维面。 | 设计中写死本期策略：保留旧 `DefaultSegmentOptions` 的全部常量，`companion_segment_delay_ms` 要么删除，要么明确只覆盖 BaseDelay，且缺省值为 400ms；任何其他可调参数另开 Story。补 L1 fake clock 断言首段/后续边界与该字段映射。 | 否。 |
| **S04-CC-P4** | **P2（兼容 Lexer 的误拆回归）** | S04 将 `[[ s E N D ]]`、全角/方头括号等都识别为控制符（S04:137-141）。旧 bot 只接受精确 `[[SEND]]`，因此这些文本曾作为普通正文出站。 | 更宽兼容会把用户/模型“解释 marker 本身”的正文拆开；code/escape 保护降低风险，但引用、HTML/entity、Unicode 空白和嵌套 list 的真实样本仍可能超出规则。 | 不建议继续扩大自然语言或单括号 inline 匹配；保持现有 fail-closed 边界。除 AT-14/15 外，增加 golden 表：真实精确 marker、每种新兼容 marker、普通教程文本、quoted/list、emoji/CJK、零宽字符、NBSP、fence/inline code/反斜杠奇偶；断言 `storage_text`、`segmenter_input` 和最终段三者。L4 使用专用测试 App，避免让模型随机产生 marker 作为唯一证据。 | 否。 |
| **S04-CC-P5** | **P2（输出来源/最终答复多次到达）** | S04:121 写“仅保留最新的 final_answer”，而旧 bot 一轮只有一个 `ExecuteResult.Text`。 | App Server 若出现重复/多条 completed final，后到但质量更差的结果会覆盖先到 final；现有 work Projection 也有 final overwrite。 | 明确“同 item.id 去重，first final wins 或 last final wins”的协议理由，并在 fake App Server 中分别投递重复相同 ID、不同 ID 的多 final，再验证最终发送/持久化内容一致且只发一次。若真实 S03 原始样本已保证唯一 final，可在这里引用该不变量。 | 否（建议 last non-empty final wins，与当前文字一致）。 |

## 4. 测试方案审查

### 已覆盖得较好

- S04-AT-11 至 AT-15 已覆盖终态前零出站、marker-only、错误继续、取消、限流重试、Unicode、code/escape、兼容 marker 与 L4 真飞书链路（S04:216-220, 237-246, 252-255）。
- 对旧 `SplitSegments` 的核心行为已有源测试：精确 marker、首尾空段、fallback、短段合并、rune/emoji、延迟边界（`segment_test.go:11-220, 358-511`）。
- 对旧 worker 的逐段发送也有测试：保序、发送错误继续、取消、限流单次重试（`worker_test.go:166-388`）。

### 测试方案还需补足

1. **补 S04-CC-P1 的 storage-finalize 失败矩阵（阻塞项）**：至少覆盖“0/1/N 段成功 + DB transient/permanent failure”，并断言 `SendText` 次数不会随 DB 重试增加。
2. **补 Lexer + Segmenter 的组合 golden 测试**：不能只对 Lexer 或旧 `SplitSegments` 单测；必须验证 internal token 永不出站、受保护的精确旧 marker 不会被下游 `strings.Split` 二次拆开。
3. **补 delivery context 生命周期测试**：`turn/completed` 后 turn context 结束，但分段应使用独立 delivery context；`/cancel`/`/new`/worker stop 取消它时停止余段。需 fake clock 和可控 sender，禁止以真实 sleep 测时。
4. **补失败文案一次性测试**：missing-final、marker-only、failed/interrupted/timeout/runtime exit 各只发送一条安全文案，且从不误发送暂存 final；与“只展示 final answer”产品措辞保持一致。
5. **L4 证据拆分**：用至少两条新 trace：一条验证成功多段送达和第一 ID/trace，另一条验证无卡/失败文案或 rate-limit（若测试 App 可控）。不要用同一成功样本推导失败路径成立。

## 5. 未证实项与审查边界

- 未能在此主机执行旧 bot `go test ./internal/session`（Go 工具链版本不足）；没有据此声称其测试当前为绿。
- 未验证 S04 计划中的 Feishu `SendText` API 是否支持请求级 idempotency key；本报告按“没有 durable per-segment idempotency”做保守设计判断。
- 未验证 App Server 是否可能为同一 Turn 发出多条不同 `final_answer` completed Item；S04 应通过 fake fixture 决定明确策略，并以 S03 原始样本补充事实。
- 未评审 CardKit JSON 2.0 的具体 API/权限；那属于 work-mode 飞书技术评审，不是本报告的 CC companion 迁移范围。

## 6. 给汇总评审的结论

S04 companion 的**用户可见多消息行为**与 CC Workspace Bot 的核心已对齐，并对模型不稳定 marker 做了受控增强。建议在 Ready → 开发前先修复 **S04-CC-P1**，并请用户明确 **S04-CC-P2**：是否把旧 companion 的 persona 输出过滤效果作为 Codex-native 后续能力保留。其余发现可作为实现测试与配置文字的完善项，不要求回退 S04 的终态 final-only 设计。
