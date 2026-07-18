# S06 Schedule Instruction and Feishu Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve the Agent-supplied autonomous instruction in schedule tools and make every outbound Feishu API call diagnosable at Info level without logging content or credentials.

**Architecture:** `schedule.create` and `schedule.update` retain their `prompt` field, but its JSON-schema description tells the Agent how to turn the current request into a future-independent user task instruction; the persisted encrypted payload remains that value and the dispatcher adds no prompt text. The Feishu sender will emit one structured result event per SDK call through a shared, redacting response classifier that retains operation, app ID, HTTP/API code, parsed subcode, Feishu request log ID, duration, and outcome.

**Tech Stack:** Go, `larksuite/oapi-sdk-go/v3`, Go `slog`, existing Go unit tests.

---

### Task 1: Specify autonomous schedule instructions in the dynamic-tool contract

**Files:**

- Modify: `internal/codexapp/processor.go:440-447`
- Modify: `internal/scheduleaction/service.go:111-119,291-299,404-427`
- Modify: `internal/codexapp/s06_catalog_test.go`
- Modify: `internal/scheduleaction/service_test.go`
- Modify: `docs/story/S06-定时任务与Agent工具-设计.md`

- [ ] **Step 1: Write failing schema and adapter tests**

  Assert that `schedule.create` requires `prompt`, and that its JSON-schema description directs the Agent to construct a future-independent user command: preserve the requested action, deliverable and destination, resolve “我/你” to the current Feishu conversation, and never reduce it to a reminder. Assert the same description is present in `schedule.update`.

  ```go
  if !strings.Contains(string(seen["schedule.create"].InputSchema), `未来执行的 Agent 不会看到创建任务时的对话上下文`) {
      t.Fatal("schedule.create prompt lacks future-context instruction")
  }
  call := codexapp.ToolCall{Tool: "create", Arguments: json.RawMessage(`{"kind":"prompt","prompt":"请检查 /workspace/drafts，选择修改时间最新的文档，创建飞书文档并将链接发送到当前飞书会话。","cron_expression":"0 9 * * *","silent":false}`)}
  result, err := service.Execute(context.Background(), route, call)
  if err != nil || !result.Success || string(store.createDraft.Payload) != "请检查 /workspace/drafts，选择修改时间最新的文档，创建飞书文档并将链接发送到当前飞书会话。" {
      t.Fatalf("result=%#v payload=%q err=%v", result, store.createDraft.Payload, err)
  }
  ```

- [ ] **Step 2: Run the focused tests and confirm RED**

  Run: `go test ./internal/codexapp ./internal/scheduleaction -run 'Instruction|S06DynamicTools' -count=1`

  Expected: FAIL because no schema or adapter field named `instruction` exists.

- [ ] **Step 3: Implement the schema description without changing dispatch content**

  Keep the dynamic-tool argument and response field named `prompt`. Define its JSON-schema description in Chinese: `未来执行的 Agent 不会看到创建任务时的对话上下文。请将当前用户请求改写为一条可独立执行的用户任务指令：使用“请……”的用户口吻，保留动作、输入位置、筛选规则、交付物和交付渠道；将“发给我/给我”明确为“发送到当前飞书会话”，避免使用“你、我、这里、刚才”等依赖当前对话的指代；不得增加用户没有要求的目标、步骤或系统说明。` Keep `PromptDispatcher` passing the decrypted payload unchanged as `Query`.

  ```go
  "prompt": {
      "type": "string",
      "minLength": 1,
      "maxLength": 16384,
      "description": "未来执行的 Agent 不会看到创建任务时的对话上下文。请将当前用户请求改写为一条可独立执行的用户任务指令：使用“请……”的用户口吻，保留动作、输入位置、筛选规则、交付物和交付渠道；将“发给我/给我”明确为“发送到当前飞书会话”，避免使用“你、我、这里、刚才”等依赖当前对话的指代；不得增加用户没有要求的目标、步骤或系统说明。"
  }
  ```

- [ ] **Step 4: Run the focused tests and confirm GREEN**

  Run: `go test ./internal/codexapp ./internal/scheduleaction -run 'Instruction|S06DynamicTools' -count=1`

  Expected: PASS.

- [ ] **Step 5: Update the S06 contract documentation**

  State that a Prompt task payload is the complete Agent-supplied user instruction and that no scheduler-owned prompt prefix/suffix is allowed.

### Task 2: Emit a safe Info result for every outbound Feishu SDK call

**Files:**

- Modify: `internal/feishu/feishu.go:132-680`
- Modify: `internal/feishu/feishu_internal_test.go` or create `internal/feishu/observability_test.go`

- [ ] **Step 1: Write failing response-classification tests**

  Test a helper that accepts a `larkcore.CodeError` response and returns only safe fields. It must parse `ErrCode: 11310` as a numeric subcode, preserve `error.log_id`, classify the outcome as `rejected`, and never return the raw `msg`, request body, target ID, message ID, card ID, file key, document ID, or token.

  ```go
  details := feishu.APIResultDetails("im.message.create", larkcore.CodeError{
      Code: 230099,
      Msg: "Failed to create card content, ext=ErrCode: 11310; ErrMsg: element exceeds the limit",
      Err: &struct{ LogID string `json:"log_id,omitempty"` }{LogID: "platform-log"},
  }, nil)
  if details.Code != 230099 || details.Subcode != 11310 || details.LogID != "platform-log" || details.Outcome != "rejected" {
      t.Fatalf("details=%#v", details)
  }
  if strings.Contains(details.String(), "element exceeds") {
      t.Fatalf("unsafe detail leaked: %s", details.String())
  }
  ```

- [ ] **Step 2: Run the focused test and confirm RED**

  Run: `go test ./internal/feishu -run 'APIResultDetails|FeishuAPI' -count=1`

  Expected: FAIL because the classifier and Info event do not exist.

- [ ] **Step 3: Implement one redacting Info event per outbound SDK call**

  Add a private helper that records `event=feishu_api_call`, `operation`, `app_id`, `outcome`, `code`, `subcode`, `log_id`, and `duration_ms` via `slog.Info`. Invoke it after every SDK call in `Sender`: attachment download, text/card/file/image message creation, file/image upload, document create/convert/write/read/owner transfer, CardKit create/update, and IM message patch. Record the event on success, API rejection, transport failure, and missing-response failure. Do not log request/response bodies or raw resource identifiers.

  ```go
  started := time.Now()
  resp, err := s.client.Im.Message.Create(ctx, req)
  s.logAPICall("im.message.create", started, codeErrorFrom(resp), err)
  ```

- [ ] **Step 4: Run the focused tests and confirm GREEN**

  Run: `go test ./internal/feishu -run 'APIResultDetails|FeishuAPI' -count=1`

  Expected: PASS.

### Task 3: Verify cross-package behavior and live process diagnostics

**Files:**

- Modify: `docs/story/S06-本地集成验证-2026-07-13.md`
- Modify: `progress.md`

- [ ] **Step 1: Run regression and static gates**

  Run:

  ```bash
  gofmt -w internal/codexapp/processor.go internal/scheduleaction/service.go internal/codexapp/s06_catalog_test.go internal/scheduleaction/service_test.go internal/feishu/feishu.go internal/feishu/observability_test.go
  go test ./internal/codexapp ./internal/scheduleaction ./internal/feishu ./internal/worker -count=1
  go test -race ./internal/codexapp ./internal/scheduleaction ./internal/feishu ./internal/worker -count=1
  go vet ./...
  go build -o runtime/codex_workspace_bot_s06 ./cmd/server
  git diff --check
  ```

  Expected: all commands pass.

- [ ] **Step 2: Restart only after the build and tests are green**

  Verify the old process command line, send `TERM`, start the rebuilt binary using the existing S06 runtime environment, then confirm the PID changed, `/healthz` is `ok`, and all configured receivers reconnect.

- [ ] **Step 3: Reproduce a normal Feishu message and inspect the safe Info events**

  Send one ordinary test request in the existing test conversation. Confirm its response path logs one `feishu_api_call` Info event per outbound SDK call, and, if a card rejection occurs, includes `operation=im.message.create`, `code=230099`, parsed `subcode`, and Feishu `log_id` without any card or user content.

- [ ] **Step 4: Record evidence and residual risk**

  Document the exact runtime PID/start time, health/receiver state, the successful schema path, and any observed Feishu error classification. Do not claim the historical `230099` root cause has been recovered; it was logged without its required diagnostics.
