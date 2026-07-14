# Attachment Original Filename Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Store each newly downloaded attachment under its sanitized original file name while retaining UUID directory isolation and safe cleanup.

**Architecture:** `attachment.Processor` will choose the final leaf name from the existing safe display-name policy instead of the literal `payload`. The cleaner will receive the stored session and original name from MySQL, validate the leaf and UUID-level ownership against that metadata, and remove only the verified attachment directory; legacy `payload` records remain valid.

**Tech Stack:** Go, MySQL, standard-library filepath/UUID-compatible identifiers, Go tests.

---

### Task 1: Capture the desired persisted path with failing processor tests

**Files:**
- Modify: `internal/attachment/processor_test.go:125-165`

- [ ] **Step 1: Require a file's final leaf name to be its original name**

```go
if got := filepath.Base(result.RelativePath); got != "report.txt" {
	t.Fatalf("stored leaf = %q, want report.txt", got)
}
```

- [ ] **Step 2: Require filename sanitization to hold at the disk boundary**

```go
if got := filepath.Base(result.RelativePath); got != "report.txt" {
	t.Fatalf("stored leaf = %q, want sanitized report.txt", got)
}
if strings.Contains(result.RelativePath, "..") {
	t.Fatalf("stored path escapes attachment directory: %q", result.RelativePath)
}
```

Use `OriginalName: "../report.txt"` in this case.

- [ ] **Step 3: Run the focused RED test**

Run: `go test ./internal/attachment -run 'TestMaterialize.*OriginalFilename' -count=1`

Expected: FAIL because the implementation still ends every path in `payload`.

### Task 2: Materialize files under a safe original filename

**Files:**
- Modify: `internal/attachment/processor.go:61-113`
- Test: `internal/attachment/processor_test.go:125-165`

- [ ] **Step 1: Select and use one safe final leaf name**

```go
name := safeDisplayName(input.OriginalName)
part := filepath.Join(dir, name+".part")
payload := filepath.Join(dir, name)
```

Keep the existing atomic `os.Rename(part, payload)` flow and return `payload` through `RelativePath`.

- [ ] **Step 2: Run the focused GREEN test**

Run: `go test ./internal/attachment -run 'TestMaterialize.*OriginalFilename' -count=1`

Expected: PASS.

- [ ] **Step 3: Run the attachment processor tests**

Run: `go test ./internal/attachment -run 'Test(Materialize|Service)' -count=1`

Expected: PASS, including image `localImage` and normal-file manifest paths.

### Task 3: Extend the cleanup projection and prove new/legacy cleanup safety

**Files:**
- Modify: `internal/storage/attachments.go:19-47`
- Modify: `internal/attachment/cleanup.go:65-77`
- Modify: `internal/attachment/cleanup_test.go:36-81`
- Test: `internal/storage/attachments_test.go`

- [ ] **Step 1: Write failing cleaner tests for named files and malformed metadata**

Use a candidate with `ID: "attachment-1"`, `SessionID: "session-1"`, `OriginalNameSafe: "report.txt"`, and a path ending in `session-1/attachment-1/report.txt`; assert `Cleaner.Run` removes the attachment directory. Add a malformed candidate whose leaf is `other.txt`; assert it is restored and its directory remains.

- [ ] **Step 2: Run the focused RED tests**

Run: `go test ./internal/attachment -run 'TestCleaner.*(Named|Malformed)' -count=1`

Expected: the named-file test FAILS because cleanup currently accepts only `payload`.

- [ ] **Step 3: Project ownership metadata from storage**

```go
type ExpiredAttachment struct {
	ID, State, WorkspaceDir, RelativePath, SessionID, OriginalNameSafe string
}
```

Select and scan `a.session_id` and `a.original_name_safe` with the existing expiry query.

- [ ] **Step 4: Validate before removing the parent directory**

```go
leaf := filepath.Base(payload)
if leaf != "payload" && leaf != safeDisplayName(candidate.OriginalNameSafe) {
	return errors.New("attachment payload name is invalid")
}
attachmentDir := filepath.Dir(payload)
if filepath.Base(attachmentDir) != candidate.ID || filepath.Base(filepath.Dir(attachmentDir)) != candidate.SessionID {
	return errors.New("attachment path is invalid")
}
return os.RemoveAll(attachmentDir)
```

- [ ] **Step 5: Run cleaner and storage tests**

Run: `go test ./internal/attachment ./internal/storage -count=1`

Expected: PASS; legacy `payload`, new named files, and malformed-path refusal are covered.

### Task 4: Format, validate, and apply the running build

**Files:**
- Modify: `internal/attachment/processor.go`
- Modify: `internal/attachment/cleanup.go`
- Modify: `internal/storage/attachments.go`
- Modify: `internal/attachment/processor_test.go`
- Modify: `internal/attachment/cleanup_test.go`

- [ ] **Step 1: Format and run targeted race coverage**

Run: `gofmt -w internal/attachment/processor.go internal/attachment/cleanup.go internal/storage/attachments.go internal/attachment/processor_test.go internal/attachment/cleanup_test.go`

Run: `go test -race ./internal/attachment ./internal/storage -count=1`

Expected: PASS.

- [ ] **Step 2: Run repository static and full test checks**

Run: `go vet ./... && go test ./...`

Expected: PASS.

- [ ] **Step 3: Build and restart through the approved controller**

Run: `./bot_controller.sh build`

Run: `./bot_controller.sh restart`

Expected: both commands succeed; the new Bot process uses the rebuilt binary and current configuration.

- [ ] **Step 4: Confirm live readiness and persisted compatibility**

Run: `curl --fail --silent --show-error http://127.0.0.1:8080/readyz`

Run a read-only MySQL query confirming existing ready attachment rows retain their historic `relative_path` ending in `payload` or their new safe original names.

Expected: readiness passes and no historical path is renamed.
