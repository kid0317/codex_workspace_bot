# Story 06 Independent Acceptance Review

Date: 2026-06-28

Scope:

- Story: `docs/06-framework-scaffold-story-design.md`
- Test design: `docs/09-framework-scaffold-test-design.md`
- Test design review: `docs/10-framework-scaffold-test-design-review.md`
- Codebase: current uncommitted working tree

## Final Acceptance Conclusion

Conclusion after remediation: **accepted for local mock-scaffold dev/test use.**

No reviewer found a P0 blocker. The implementation builds, tests, races, and
smokes successfully as a local mock-backed scaffold. The acceptance remediation
closed the repeated P1 findings around task/runtime correctness, operational
hardening, runbook consistency, debug safety defaults, dependency inventory,
and startup evidence hygiene.

Story 06 remains explicitly scoped to a mock-backed local scaffold. Real Feishu
networking, real Codex app-server protocol support, production deployment, and
full release compliance remain follow-up scope.

## Remediation Summary

Closed items:

- Malformed task YAML no longer prevents app bootstrap when valid tasks exist.
- System tasks validate engine event terminal status and do not update
  `LastRunAt` on failed or malformed streams.
- Runtime close cancels active turns instead of only waiting for request-owned
  contexts.
- HTTP server now has baseline timeouts and max header size.
- Debug task IDs must use canonical `{app_id}/{slug}`.
- Duplicate event receipts are pruned by configured TTL.
- Debug token comparison uses constant-time comparison.
- `config.yaml.template` defaults to `debug_enabled: false`.
- README debug examples include `X-Debug-Token`.
- `start.sh` provides a local start path and explicit debug opt-in.
- Smoke evidence directory is replaced per run and debug is enabled only for
  the smoke config.
- Feishu app IDs are redacted from rendered config output.
- Third-party notices now include the full `go list -m all` module graph.
- `docs/04-decisions.md` now reflects that Story 06 implements only the
  mock-backed app-server-shaped boundary.

## Verification Executed By Coordinator

All commands were run from `/root/codex_workspace_bot`.

```bash
go test ./...
go vet ./...
go build ./...
gofmt -l .
go test ./... -count=1
go test -race -count=1 ./internal/session ./internal/task ./internal/approval ./internal/db ./internal/debugapi ./internal/config ./internal/workspace ./internal/app
bash scripts/story06_smoke.sh
```

Results:

- `go test ./...`: pass
- `go vet ./...`: pass
- `go build ./...`: pass
- `gofmt -l .`: no output
- `go test ./... -count=1`: pass
- race subset: pass
- smoke: pass, evidence written under `docs/evidence/story06/latest/`

Smoke evidence observed:

- `/health`: `{"status":"ok"}`
- `/debug/dispatch`: `{"ok":true}`
- `/debug/task/run`: `{"ok":true}`
- SQLite messages include `user|hello` and `assistant|hello`
- SQLite turns include completed turns
- debug disabled route returns 404

## Independent Role Conclusions

| Role | Conclusion | Main reason |
|---|---|---|
| Project management | Conditional pass | Basic delivery works, but manual acceptance evidence is incomplete |
| Product management | Conditional pass | Product boundary is mostly right, but approval closure and docs/runbook drift remain |
| Technical architecture | Conditional pass | Architecture direction is sound, but task scan, shutdown, and system task terminal handling need fixes |
| QA | Conditional pass | Build/test/race/smoke pass, but multiple `09`/`10` coverage obligations remain unclosed |
| Security | Conditional pass | No P0 or secret leak found, but HTTP hardening is needed before non-local/shared use |
| Legal/compliance | Conditional pass | Local mock use is acceptable, but license inventory and data-retention policy are insufficient for production |
| Operations/SRE | Conditional pass | Dev/test mock operation is acceptable, but not production/real-integration ready |

## Initial P1 Findings And Current Status

1. **Manual acceptance evidence is incomplete.** Status: partially closed.

   `docs/09-framework-scaffold-test-design.md` requires recorded evidence for
   manual verification cases, full fixture usage, critical integration flows,
   and old parity classification. Current smoke evidence covers only a single
   demo app health/dispatch/task/SQLite/debug-disabled path.

   Remediation improved smoke hygiene and runbook consistency. Broader manual
   matrix evidence remains recommended before promoting beyond local dev/test.

2. **Task YAML scan can block startup on a single malformed task file.** Status: closed.

   `internal/app/app.go` treats `task.ScanDir` errors as bootstrap-fatal, while
   `internal/task/watcher.go` returns accumulated parse errors. This is risky
   for old workspaces where one bad task YAML should not prevent the app from
   starting or preserving last-known-good tasks.

3. **Graceful shutdown does not actively cancel an in-flight turn.** Status: closed.

   `session.Manager.Close` closes worker stop channels and waits, but an
   active `dispatchNow` continues on the original request context. The runtime
   may return after timeout while a worker goroutine is still running.

4. **System task execution does not validate engine terminal status.** Status: closed.

   `internal/task/runner.go` drains the stream and updates `LastRunAt` if the
   stream has no Go error, even if the engine emitted failed/interrupted or
   malformed terminal events.

5. **Approval behavior is not a user-visible closed loop.** Status: scoped.

   The current implementation persists pending approval and stops before
   output, which is good for safety, but `/debug/approval/respond` only updates
   DB status. It does not resume or complete the pending turn. This may be
   acceptable only if Story 06 explicitly scopes approval to state-machine
   persistence rather than full execution continuation.

   Story 06 keeps approval as safe state-machine persistence and debug status
   response. Resuming a pending engine turn after approval remains follow-up
   app-server behavior.

6. **HTTP server lacks baseline operational hardening.** Status: closed.

   `cmd/server/main.go` constructs `http.Server` with only `Addr` and
   `Handler`. Before any shared dev/test or non-local binding, add
   `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, and
   `MaxHeaderBytes`.

7. **Runbook and configuration are inconsistent for debug token use.** Status: closed.

   Debug APIs require a token, but README manual curl examples omit
   `X-Debug-Token`. The template also defaults to `debug_enabled: true` with a
   fixed example token.

8. **Test coverage does not yet satisfy the full `09`/`10` risk matrix.** Status: partially closed.

   QA identified missing or weak coverage for:

   - Feishu old receiver regressions: invalid JSON fallback, nil/safe string,
     welcome message variants.
   - Companion output dirty-message corpus and filter gates.
   - Segment rate-limit retry and cancellation.
   - Task watcher parse-error cache, task ID migration, cleanup wrong-app
     isolation.
   - Observability stable IDs, row merge/split, atomic state, per-turn
     fail-open.

9. **Compliance is acceptable only for local mock use.** Status: closed for Story 06 scope.

   The third-party notice is incomplete relative to `go list -m all`, and
   message/task/approval/turn history retention is mostly manual.

   Dependency inventory and README data deletion guidance were added. A
   machine-checked license report remains required before production release.

## P2 Findings

- `internal/store` package described in design is implemented inside
  `internal/db`; either document this design deviation or split the package.
- `duplicate_message_ttl_hours` is now implemented as TTL cleanup for event
  receipts.
- Debug task ID validation now requires canonical `{app_id}/{slug}`.
- Scheduler cron validation is too permissive/implicit.
- Queue overflow now records a rejected turn, sends busy text, and emits
  `dispatch_rejected`.
- Smoke replaces the evidence directory per run to avoid stale `curl.err`
  startup retry noise.
- Smoke artifact cleanliness is recorded but not asserted.
- `RedactedString` now redacts Feishu app IDs.
- Debug token comparison now uses constant-time compare.
- Production readiness needs DB backup/migration version gates and rollback
  rehearsal.

## Accepted Scope Boundaries

The reviewers agree these are **not** current Story 06 blockers if kept as
explicit follow-up scope:

- Real Feishu WebSocket receiver.
- Real Feishu SDK send/approval card UI.
- Real Codex app-server protocol client.
- Production tracing/metrics and ingress deployment.

`docs/04-decisions.md` now records that Codex app-server is the target primary
engine, while Story 06 delivers only the mock-backed app-server-shaped boundary.

## Recommended Acceptance Decision

Use this decision:

> Story 06 is accepted as a runnable local mock scaffold baseline. It is not a
> production or real-integration release; real Feishu, real Codex app-server,
> and production compliance gates remain follow-up work.

Recommended next step:

1. Keep expanding manual evidence toward the full `09` matrix.
2. Move real Feishu and real Codex app-server work into follow-up stories.
3. Add production release gates before any non-local deployment.
