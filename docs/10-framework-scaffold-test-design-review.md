# Framework Scaffold Test Design Review

Date: 2026-06-28

Reviewed target:

- `docs/09-framework-scaffold-test-design.md`
- `docs/06-framework-scaffold-story-design.md`

Reference source:

- `/root/cc_workspace_bot` tests and compatibility assets

## Overall Summary

`09-framework-scaffold-test-design.md` is directionally strong. It correctly
turns the scaffold story into testable areas: legacy config, workspace init,
DB compatibility, session/channel behavior, task contract, attachments,
`SESSION_CONTEXT.md`, output shaping, mock engine events, approval state, and
debug API safety.

The main gap is not broad coverage. The remaining risk is that several tests
are still specified as high-level assertions instead of precise regression
contracts. The old `cc_workspace_bot` test suite contains detailed historical
bug guards around Feishu text parsing, companion filtering, segment sending,
rate-limit retries, attachment path relocation, task watcher parse-error
deduplication, task ID migration, cleanup isolation, and observability
fail-open behavior. Those should be pulled into `09` explicitly so the new
implementation does not pass broad scaffold tests while regressing old edge
cases.

## Role 1: Technical Architect Review

### 1. Overall Judgment

From an architecture standpoint, `09` mostly matches the story boundary in
`06`: real app-server and real Feishu network are out of scope, while the Go
framework behavior is testable through mocks and fixtures. The package-level
unit tests and mock-backed integration tests are aligned with the runtime
architecture.

The key missing architecture tests are around cross-cutting contracts:
repository boundaries, lifecycle cancellation, observability schema stability,
per-app runtime isolation, and failure-mode state transitions. These are the
places where a runnable scaffold can look correct but become hard to evolve
when the real app-server client is added.

### 2. Covered Architecture Areas

- Config and legacy app loading are covered.
- Workspace initialization is covered with idempotency and non-overwrite tests.
- DB additive migration and physical `claude_session_id` compatibility are
  covered.
- Session worker serialization, `/new`, work/companion mode, and queue
  overflow are covered.
- Task routing and scheduler behavior are covered.
- Mock engine stream events include happy path and non-happy-path scenarios.
- Debug API safety has explicit disabled, unknown app, oversized body, path
  injection, and secret-echo tests.

### 3. High-Priority Architecture Gaps

- **Repository boundary enforcement is too soft.** `09` says domain packages
  should use repositories, but it does not define an enforceable test. Add a
  static or package-level test that prevents direct GORM usage outside
  `internal/db` and `internal/store`.

- **Runtime topology isolation needs tests.** `06` requires per-app app-server
  runtime paths under `codex.app_server.runtime_dir/{app_id}`. `09` tests
  config resolution but does not require a cross-app isolation test proving app
  A cannot resolve or use app B runtime dir, socket path, or auth token.

- **Lifecycle shutdown tests are incomplete.** `09` tests approval timeout and
  queue overflow, but it should require graceful shutdown while a slow mock
  stream, pending approval, pending task, and segment sending are active.

- **Observability contract is underspecified.** The old Langfuse dry-run tests
  validate stable trace grouping, usage handling, fail-open behavior, and
  per-turn error isolation. `09` requires logs with IDs, but not stable event
  names or stable JSON fields that downstream telemetry can consume.

- **Engine event stream state machine needs table-driven invalid sequence
  tests.** `09` covers malformed events and terminal rules, but should spell
  out invalid sequences: delta before start if start is required by scenario,
  approval after terminal, completed then failed, interrupted then delta, and
  stream close without terminal.

### 4. Suggested Architecture Test Additions

- Add `internal/architecture` or repository-boundary tests:
  - Fail if `gorm.io/gorm` is imported by `internal/session`,
    `internal/task`, `internal/attachment`, `internal/approval`,
    `internal/debugapi`, or `internal/output`.
  - Allow only `internal/db`, `internal/store`, and test helpers to import GORM.

- Add per-app runtime isolation tests:
  - Given two apps, resolved runtime dirs must be siblings under
    `runtime_dir`, never derived from `workspace_dir`.
  - Debug requests for app A cannot set or override app B runtime path.
  - Auth token fields are not written to DB, logs, debug responses, or
    `SESSION_CONTEXT.md`.

- Add shutdown integration tests:
  - Slow stream interrupted by process shutdown.
  - Pending approval does not block shutdown.
  - Segment sending exits on context cancellation.
  - Task scheduler stops without starting new jobs after shutdown begins.

- Add observability golden tests:
  - Each lifecycle event includes `app_id`, `channel_key`, `session_id`,
    `turn_id`, `engine_thread_id` when present, `task_id` when present,
    `status`, `duration_ms`, and error category.
  - Usage with zero values is preserved.
  - One failing turn does not prevent later turn events from being emitted.

## Role 2: Product Manager Review

### 1. Overall Judgment

From product functionality, `09` covers most user-visible migration promises:
existing apps can load, workspaces are not damaged, messages route to the right
chat/thread, companion mode keeps its user-facing style, `/new` resets context,
attachments survive the common "send file then ask" flow, and tasks keep
running.

The product risk is that a few old user-facing details are only implied:
welcome text, companion tone for acknowledgements, Feishu post message parsing,
rate-limit retry behavior, and exact manual evidence for "all existing apps can
start." These details matter because they are visible to real users even though
they are not core engine behavior.

### 2. Covered Product Behaviors

- Work mode sends thinking first and updates the final card.
- Companion mode sends direct text and supports `[[SEND]]` segmentation.
- `/new` archives current session and starts fresh engine continuity.
- Attachment-only events are acknowledged and later merged into a text prompt.
- User-facing, borrow-channel, and system tasks have separate expected
  behavior.
- Manual cases require SQLite evidence, mock sender evidence, and startup logs.

### 3. High-Priority Product Gaps

- **Companion acknowledgement tone is not explicitly tested.** Old tests
  `TestAttachmentAckText` and `TestNewSessionReceipt` ensure companion mode
  does not leak operational language such as "已收到", "请描述", "会话", or
  "session". `09` should keep that as a product regression test.

- **Welcome message behavior is missing.** Old Feishu tests verify bot-added
  and user-added welcome messages include the app ID, group/member names, and
  `/new`, with fallbacks for missing group or user names. If the new bot keeps
  group lifecycle handling, the tests should remain.

- **Feishu rich post text extraction is missing.** Old `TestExtractPostText`
  covers title, multi-line content, ignored non-text tags, and invalid JSON
  fallback. Without this, product users may see lost text from rich messages.

- **Rate-limit retry is missing.** Old companion segment sending retries Feishu
  rate-limit code `99991400` once and continues with later segments. `09`
  covers failed sends generally but not this product-critical delivery behavior.

- **Manual full-app startup should be a named gate.** `09` requires a redacted
  28-app fixture, but manual verification should also include a named case that
  initializes all app workspaces and captures app count, mode distribution, and
  per-app init status.

### 4. Suggested Product Test Additions

- Add companion tone tests:
  - Attachment ack in companion mode must avoid operational wording.
  - `/new` receipt in companion mode must avoid technical/session wording.
  - Work mode may keep instructional ack text.

- Add Feishu lifecycle message tests:
  - Bot added to group includes app ID, group name or fallback, and `/new`.
  - User added welcome includes user names or fallback and app ID.

- Add rich message tests:
  - Post content extraction preserves title and text lines.
  - Invalid post JSON falls back to raw content.
  - Image/link/non-text tags do not pollute the prompt.

- Add delivery behavior tests:
  - Companion segment rate-limit retry on code `99991400`.
  - Segment sending continues after non-rate-limit error.
  - Work-mode card failure falls back to one plain text message without
    companion segmentation.

- Add manual `MAN-00: 28-App Compatibility Smoke`:
  - Load redacted legacy config.
  - Initialize all configured workspace copies.
  - Confirm no secret in logs.
  - Confirm app total, work/companion counts, and provider set.
  - Confirm every app reaches "ready with mock engine" status.

## Role 3: cc_workspace_bot Test Lead Review

### 1. Overall Judgment

`09` already covers many capabilities that the old Go test suite protected,
especially config loading, workspace init, session archive, segmentation,
task YAML contract, task scheduling, cleanup, DB open/migrate, receiver
channel keys, and output filtering.

However, several old regression tests are more precise than the current
`09` wording. The new test design should reference these behaviors directly.
The goal is not to port old engine tests wholesale. Old core-engine execution
tests should be translated only when they protect non-engine behavior such as
routing context, workspace boundaries, provider config preservation, environment
redaction, transcript parsing semantics, or observability fail-open behavior.

### 2. Already Covered Old Test Assets

- `internal/config/config_test.go`
  - `TestValidate`
  - `TestAllowedChat`
  - `TestLoad_ValidYAML`
  - `TestLoad_Defaults`
  - `TestLoad_FileNotFound`
  - `TestLoad_ProviderModelConfig`

  Covered by `09` config tests for legacy config, defaults, allowed chats,
  providers, app-level overrides, and secret redaction.

- `internal/workspace/init_test.go`
  - `TestInit_CreatesRequiredDirs`
  - `TestInit_CreatesMemoryLock`
  - `TestInit_DoesNotOverwriteExistingLock`
  - `TestInit_Idempotent`
  - `TestInit_TemplateDoesNotOverwriteExisting`

  Covered by workspace init tests, except symlink handling needs an explicit
  carry-over test.

- `internal/db/db_test.go`
  - `TestOpen_CreatesTablesAndMigrates`
  - `TestOpen_Idempotent`
  - `TestOpen_InvalidPath`

  Covered by DB fixture migration and idempotent open tests. Invalid path should
  be explicitly retained.

- `internal/session/manager_test.go`
  - `TestArchiveChannel_MarksActiveArchived`
  - `TestArchiveChannel_Idempotent`
  - `TestArchiveChannel_OnlyTouchesActive`
  - `TestArchiveChannel_NoActiveSession`

  Covered by `/new`, `post_archive`, and archive semantics, but should be named
  as direct unit tests for the archive service.

- `internal/session/segment_test.go`
  - `TestSplitSegments`
  - `TestSplitSegments_Extra`
  - `TestTypingDelay`

  Covered by output segmentation goals, but old edge cases should be preserved
  as golden/table tests.

- `internal/session/filter_test.go`
  - `TestReadFilteredReply`
  - `TestFilterCanary`
  - `TestApplyOutputFilter`
  - `TestAttachmentAckText`
  - `TestNewSessionReceipt`

  Partly covered. `09` covers filter fallback and empty output, but needs
  canary and companion tone tests.

- `internal/session/worker_test.go`
  - `TestWorker_CompanionMultiSegment`
  - `TestWorker_NonCompanionUnchanged`
  - `TestWorker_SegmentContinueOnError`
  - `TestWorker_SegmentCtxCancelled`
  - `TestWorker_PersistResultStripsDelimiter`
  - `TestWorker_RateLimitRetry`
  - `TestWorker_RateLimitRetryCtxCancelled`
  - `TestWorker_WorkModeNoCardFallback`
  - `TestReplacePaths_*`
  - `TestIsAttachmentOnly`
  - `TestAttachmentReplyText`

  Partly covered. Missing explicit rate-limit retry, work-mode fallback, path
  relocation edge cases, malformed attachment reference, and companion-tone ack.

- `internal/feishu/receiver_test.go`
  - `TestBuildChannelKey`
  - `TestReplyTarget`
  - `TestExtractPostText`
  - `TestSafeStr`
  - `TestSanitizeFilename`
  - `TestWelcomeMessageContent`

  Partly covered. Channel key and filename sanitization are covered in spirit;
  reply target, post extraction, safe string, and welcome content need explicit
  tests.

- `internal/task/runner_test.go`
  - `TestBuildChannelKey`
  - `TestParseChannelKey`
  - `TestReceiveTarget`
  - `TestLoadYAML_*`
  - `TestIsSystemTask`
  - `TestLoadYAML_PostArchive`
  - `TestSystemTaskSlug`

  Mostly covered. Add explicit unresolved placeholder and legacy parse fallback
  tests.

- `internal/task/scheduler_test.go`
  - `TestScheduler_AddFunc_Success`
  - `TestScheduler_AddFunc_InvalidCron`
  - `TestScheduler_Add_AndRemove`
  - `TestScheduler_Add_ReplacesExisting`
  - `TestScheduler_Remove_NonExistent`

  Covered by scheduler tests, but replace-existing and remove-nonexistent
  should be named.

- `internal/task/watcher_test.go`
  - `TestShouldLogParseErr_DedupAndHashBreakthrough`
  - `TestShouldLogParseErr_CapDropsNewEntries`
  - `TestForgetPath_ReallowsNewErrorsAfterSuccess`
  - `TestPruneErrCache_DropsMissingPathsOnly`
  - `TestShouldLogParseErr_ConcurrentAccess`
  - `TestHashContent_StableAndDistinct`
  - `TestClassifyTarget_AllModes`

  Mostly missing from `09`. The test design mentions malformed task YAML but
  not parse-error dedup, content-hash breakthrough, cache cap, success reset, or
  concurrent access.

- `internal/task/migration_test.go`
  - `TestMigrateTaskIDs_EmptyDB`
  - `TestMigrateTaskIDs_AlreadyMigrated`
  - `TestMigrateTaskIDs_BareName`
  - `TestMigrateTaskIDs_UUIDFilename`
  - `TestMigrateTaskIDs_LegacyDotPrefix`
  - `TestMigrateTaskIDs_ConflictDropsLegacy`
  - `TestMigrateTaskIDs_EmptyAppID`
  - `TestMigrateTaskIDs_Idempotent`
  - `TestMigrateTaskIDs_MultipleWorkspaces`

  Partly covered by canonical task ID rules. The legacy dotted-prefix,
  conflict-drop, empty app ID, and idempotency cases should be explicit.

- `internal/task/cleanup_test.go`
  - `TestCleaner_CleanApp`
  - `TestCleaner_Run_MultipleApps`
  - `TestCleaner_CleanApp_WrongApp`

  Partly covered. `09` covers attachment cleanup, but should explicitly test
  archived vs active cutoffs and wrong-app isolation.

- `tests/output_filter/*`
  - Golden extraction parity
  - operational sentence removal
  - character sentence false-positive guards
  - `[[SEND]]` invariants
  - layer 2 degradation/fail-open behavior
  - recursion/task/no-channel/init-status gates
  - turn-start snapshot behavior

  Partly covered. `09` says Go-side filter and segmentation exist, but it does
  not require old dirty-message samples, canary/gate behavior, snapshot
  precedence, or filter fail-open contracts.

- `scripts/langfuse_dryrun/tests/*`
  - usage normalization and zero preservation
  - multi-row assistant merge
  - turn splitting
  - stable trace/observation IDs
  - state offset atomicity and concurrent writes
  - fail-open on missing input/meta
  - per-turn error isolation

  Partly covered by observability goals, but not enough for regression safety.
  The new scaffold does not need to reuse old Langfuse hook code, but should
  preserve equivalent telemetry contracts.

- `workspaces/_template/.claude/skills/chat_history/test_chat_history_search.py`
  - reads channel key from `SESSION_CONTEXT.md`
  - errors on missing/empty context
  - queries only current channel
  - role, keyword, days, limit, and truncation filters
  - output includes channel key and session ID

  Not explicitly covered. Because `SESSION_CONTEXT.md` is a compatibility
  contract, `09` should include a fixture test proving existing chat-history
  skill behavior still works against the generated DB/context shape.

### 3. High-Priority Missing Old Regression Points

1. **Feishu post extraction and reply target behavior**

   Add direct tests equivalent to `TestReplyTarget` and `TestExtractPostText`.
   Current `09` only says normalize receiver inputs and channel keys.

2. **Companion output filter dirty-message corpus**

   Add golden/table tests from `tests/output_filter/test_filter_rules.py`:
   operational sentences removed, character sentences kept, `[[SEND]]` count
   never increases, leading/adjacent markers collapse safely.

3. **Filter gating and fail-open behavior**

   Preserve the old gate cases: recursion guard, task run skipped, no channel
   key skipped, init not done skipped, snapshot precedence over current memory,
   layer 2 timeout/failure degrades, internal crash exits success. In the new Go
   design these become output filter service tests rather than hook tests.

4. **Companion segment send error behavior**

   Add old worker cases: continue after a segment send error, retry rate-limit
   once, cancel during segment delay/backoff, and strip delimiter in DB history.

5. **Work-mode card fallback**

   Add `TestWorker_WorkModeNoCardFallback` equivalent: if thinking-card
   creation fails, work mode sends exactly one plain text reply and does not
   apply companion segmentation.

6. **Attachment path relocation edge cases**

   `09` covers pending attachment lifecycle, but old tests also protect prompt
   reference replacement: already moved files, malformed references, missing
   source files, image and file prefixes, multiple attachments, and empty/no
   attachment prompts.

7. **Task watcher parse-error cache**

   Malformed YAML warnings should not spam logs. Preserve dedup by path/hash/msg,
   hash breakthrough, cap behavior, forget-on-success, prune-missing-paths, and
   race-safe concurrent access.

8. **Task ID migration edge cases**

   Explicitly test bare IDs, UUID filenames, legacy dotted prefix, conflict
   resolution by dropping legacy rows, empty app ID skip, idempotency, and
   multi-workspace filename collisions.

9. **Cleanup wrong-app isolation**

   Add direct tests ensuring cleanup never deletes another app's attachment
   directory even when sharing a DB registry.

10. **Chat history skill compatibility**

   Generate `SESSION_CONTEXT.md` and legacy `bot.db` fixture, then run the
   chat-history search fixture tests or equivalent Go/Python compatibility tests.

### 4. Medium/Low-Priority Suggestions

- Keep `TestSafeStr` equivalent for nil pointer safety in receiver parsing.
- Keep `TestSanitizeFilename` cases for path traversal and backslash handling.
- Keep workspace template symlink-skip behavior if any template copy remains.
- Keep invalid DB path test.
- Add manual evidence for Feishu-style rich post and topic-thread debug events.
- Add artifact tests that include `.bak`, state, telemetry offset files, and
  filter logs if equivalent files are retained.
- For old sanitizer tests, do not port old core-engine JSONL repair directly,
  but preserve the product-level contract: generated telemetry/parser inputs
  must tolerate mixed model/provider rows, malformed rows, sidechain/subagent
  rows, and synthetic rows without crashing or leaking tool internals.

### 5. Concrete Test Entries To Add To `09`

Add these named items to `09` before implementation:

- `internal/feishu`
  - `TestReplyTarget_P2PUsesOpenID_GroupUsesChatID`
  - `TestExtractPostText_TitleLinesAndInvalidJSON`
  - `TestSanitizeFilename_PathTraversalAndBackslash`
  - `TestWelcomeMessageContent_BotAndUserAdded`

- `internal/output`
  - `TestFilterRules_OperationalSentencesRemoved_CharacterSentencesKept`
  - `TestFilterRules_SendMarkerInvariants`
  - `TestFilterGate_RecursionTaskNoChannelInitStatus`
  - `TestFilterGate_TurnStartSnapshotPrecedence`
  - `TestFilter_FailOpenOnLayer2FailureTimeoutCrash`
  - `TestFilterCanary_ArmedThresholdAndReset`

- `internal/session`
  - `TestSendResult_CompanionContinuesAfterSegmentError`
  - `TestSendResult_CompanionRateLimitRetry`
  - `TestSendResult_CancelDuringSegmentDelayAndBackoff`
  - `TestSendResult_WorkModeNoCardFallbackSingleText`
  - `TestPersistResult_StripsSendDelimiter`
  - `TestArchiveChannel_IdempotentOnlyActiveTarget`

- `internal/attachment`
  - `TestReplaceAttachmentPaths_SingleMultipleAlreadyMoved`
  - `TestReplaceAttachmentPaths_MalformedAndMissingSource`
  - `TestIsAttachmentOnly_ImageFileWhitespaceAndTextMixed`
  - `TestAttachmentAckText_WorkVsCompanionTone`

- `internal/task`
  - `TestLoadYAML_UnresolvedPlaceholder`
  - `TestTaskIDMigration_BareUUIDDottedConflictEmptyAppIDIdempotent`
  - `TestWatcherParseErrorCache_DedupHashBreakthroughCapForgetPruneRace`
  - `TestScheduler_AddReplaceRemoveNonExistent`
  - `TestSystemTaskSlug_NestedSlashFallback`

- `internal/cleanup`
  - `TestCleaner_ArchivedRetentionActiveMaxDays`
  - `TestCleaner_MultipleApps`
  - `TestCleaner_WrongAppIsolation`

- `internal/sessionctx` compatibility
  - `TestChatHistorySkill_ReadsGeneratedSessionContext`
  - `TestChatHistorySkill_FiltersCurrentChannelRoleKeywordDaysLimit`
  - `TestChatHistorySkill_ErrorsOnMissingOrEmptyChannelKey`

- `internal/observability`
  - `TestUsage_NormalizePreservesZeroAndCacheFields`
  - `TestTurnTelemetry_MergeRowsAndSplitTurns`
  - `TestTelemetryIDs_StableAndNamespaceSeparated`
  - `TestTelemetry_FailOpenAndPerTurnErrorIsolation`
  - `TestTelemetryState_AtomicConcurrentUpdates`

## Consolidated P0/P1/P2 Recommendations

### P0: Must Update Before Implementation

- Add old regression test names and expected behavior for Feishu post parsing,
  reply targets, companion filter corpus, segment send error/rate-limit/cancel,
  work-mode card fallback, task watcher parse-error cache, task ID migration,
  cleanup wrong-app isolation, and chat-history skill compatibility.
- Add enforceable repository boundary and direct-GORM import tests.
- Add graceful shutdown integration tests for slow stream, pending approval,
  pending task, and segment sending.
- Add telemetry/observability golden field tests, including zero usage
  preservation and fail-open behavior.

### P1: Should Add For Migration Confidence

- Add manual `MAN-00: 28-App Compatibility Smoke`.
- Add rich post/topic-thread manual debug cases.
- Add workspace template symlink-skip test if template copying remains.
- Add invalid DB path and unknown/malformed config fixture tests.
- Add artifact ignore coverage for any telemetry state/filter log equivalents.

### P2: Nice To Have

- Reuse old Python chat-history tests directly as compatibility tests if the
  generated `SESSION_CONTEXT.md` and `bot.db` fixture can be passed in cleanly.
- Keep old dirty-message corpus as golden files under `testdata/output_filter/`
  so product tone regressions are reviewable.
- Add a coverage matrix that maps each old `cc_workspace_bot` test file to new
  package tests, with "ported", "translated", or "intentionally retired".

## Final Judgment

`09` is a solid scaffold test design, but it should be revised once more before
development. The revision should not expand scope into the real app-server or
real Feishu network. It should make old non-engine regression contracts
explicit, especially the ones that protected user-visible companion behavior,
task reliability, attachment handling, routing context, and observability.
