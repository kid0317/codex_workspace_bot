# Story 1 Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Build a locally runnable Go service that persists configured Feishu Apps, receives text events through the Feishu WebSocket SDK, records a message round, and sends a fixed reply.

**Architecture:** `config` loads process settings and secrets from environment; `storage` owns MySQL migrations and repositories; `feishu` normalizes and sends SDK events; `router` owns idempotent round processing. Docker Compose provides local persistent MySQL. Tests use fakes for Feishu and a real local MySQL integration gate when available.

**Tech Stack:** Go 1.23, `database/sql`, MySQL 8.4, `go-sql-driver/mysql`, `larksuite/oapi-sdk-go/v3`, YAML, Docker Compose.

---

### Task 1: Bootstrap and configuration

**Files:** `go.mod`, `cmd/server/main.go`, `internal/config/*`, `config.yaml.template`, `.env.example`, `tests/config_test.go`.

- [ ] Add failing tests for defaults, environment password loading, and invalid configuration.
- [ ] Run targeted tests and observe RED.
- [ ] Implement typed YAML/environment configuration and validation.
- [ ] Run targeted tests GREEN, then `gofmt`.

### Task 2: MySQL Compose, migrations, and repositories

**Files:** `docker-compose.yml`, `internal/storage/*`, `migrations/*`, `tests/storage_*_test.go`.

- [ ] Add failing repository tests for App CRUD, ChatGroup upsert, Message idempotency, and state transitions.
- [ ] Run RED against missing storage package.
- [ ] Implement forward SQL migration runner and parameterized repositories.
- [ ] Run unit tests GREEN; start Compose and run MySQL integration tests.

### Task 3: Feishu event boundary and router

**Files:** `internal/feishu/*`, `internal/router/*`, `tests/router_test.go`.

- [ ] Add failing tests for p2p/group targets, topic-group ignore, header App validation, duplicate events, and send failures.
- [ ] Run RED.
- [ ] Implement normalized receiver interfaces, fixed-reply renderer, idempotent router, and SDK adapters.
- [ ] Run router tests GREEN and race coverage.

### Task 4: Server composition, appctl, and observability

**Files:** `cmd/server/main.go`, `cmd/appctl/main.go`, `internal/logging/*`, `tests/appctl_test.go`.

- [ ] Add failing command/service lifecycle tests.
- [ ] Run RED.
- [ ] Implement server wiring, per-App receiver lifecycle states, local health endpoint, and App CRUD CLI with non-secret output.
- [ ] Run targeted tests GREEN and launch local server with Compose.

### Task 5: Acceptance docs and final verification

**Files:** `README.md`, `docs/02-redesign-high-level.md`, `docs/story/S01-基础框架与飞书接入-设计.md`, `docs/story/STORY_LIST.md`.

- [ ] Update docs to match implemented commands, schema, limits, and residual external smoke prerequisites.
- [ ] Run `gofmt`, `go vet ./...`, `go test ./... -race`, `go build ./...`, Compose health checks, and a local repository smoke.
- [ ] Record results in Story completion evidence; leave real Feishu manual validation as the documented user-executed S01-LI-01 gate if credentials are not supplied.
