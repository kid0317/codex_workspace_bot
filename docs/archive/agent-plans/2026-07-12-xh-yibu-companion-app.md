# xh_yibu Companion App Enablement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Import the existing `xh_yibu` Feishu application as an enabled companion-mode App without changing either existing work-mode App.

**Architecture:** The current runtime loads enabled App records from MySQL at startup. A parameterized, idempotent upsert will read only the required fields from the approved legacy backup and persist its explicit companion mode, current default Codex model, and current default reasoning effort. The service will then be restarted so its receiver registry is rebuilt from the database.

**Tech Stack:** Go service, MySQL, existing `config.yaml`, Feishu WebSocket receiver.

---

### Task 1: Upsert and verify the companion App

**Files:**

- Read: `/root/cc_workspace_bot/config.yaml.bak.20260526_165800`
- Read: `config.yaml`, `.env`, `internal/storage/apps.go`
- Modify: MySQL `apps` row named `xh_yibu` only
- Read: `runtime/s04.pid`, `/healthz`, `/readyz`

- [ ] **Step 1: Validate the source and target preconditions**

Run a redacted read of the legacy entry; verify its name, Feishu App ID, and workspace directory; verify `/root/xh_yibu` exists; query current Apps without selecting secrets.

Expected: no existing `xh_yibu` row and the workspace is a directory.

- [ ] **Step 2: Perform a single transactional, idempotent upsert**

Extract the Feishu App ID and secret from the approved backup inside the command process. Upsert only `xh_yibu` with:

```text
workspace_dir=/root/xh_yibu
workspace_mode=companion
model=gpt-5.6-terra
reasoning_effort=medium
enabled=true
```

Expected: one `apps` row with exactly those non-secret fields. Existing Apps remain unchanged.

- [ ] **Step 3: Restart from the built S04 binary and verify the active process**

Stop the PID recorded in `runtime/s04.pid`, launch `runtime/codex_workspace_bot_s04 -config config.yaml`, then check `/healthz` and `/readyz`.

Expected: health is `ok`, three receiver records are connected, including `xh_yibu`, and the process list shows the intended binary and config.

- [ ] **Step 4: Record the operational evidence**

Append the timestamp, non-secret App fields, process PID, and health/receiver outcome to `progress.md`. Do not copy credentials, user message contents, or tokens.

Expected: a reproducible handoff for the forthcoming companion L4 test.
