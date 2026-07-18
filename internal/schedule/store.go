package schedule

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrTaskNotFound     = errors.New("schedule task not found")
	ErrVersionConflict  = errors.New("schedule task version conflict")
	ErrDueClaimConflict = errors.New("schedule due claim conflict")
	ErrTaskQuota        = errors.New("schedule enabled task quota exceeded")
)

type TaskKind string

const (
	TaskPrompt TaskKind = "prompt"
	TaskScript TaskKind = "script"
)

// Task exposes only tool-safe task metadata. Owner identity and payload are
// deliberately absent because list/create/update responses must not leak them.
type Task struct {
	ID             string
	Kind           TaskKind
	CronExpression string
	Timezone       string
	Silent         bool
	Enabled        bool
	Version        uint64
	NextRunAt      time.Time
}

// OwnedTask is sensitive tool-result material: Payload is returned only after
// an exact owner-scoped repository query and must never be logged or persisted
// by its caller.
type OwnedTask struct {
	Task
	Payload   string
	UpdatedAt time.Time
}

type TaskPage struct {
	Tasks []OwnedTask
	Next  *CursorPosition
}

// TaskDraft holds sensitive create input only while the trusted tool handler
// is executing. Repository callers must obtain Owner from the bound route.
type TaskDraft struct {
	ID             string
	Owner          Owner
	Kind           TaskKind
	CronExpression string
	Payload        []byte
	Silent         bool
}

// TaskPatch contains only mutable task fields. A non-nil Payload must be
// validated by the caller against the task's existing kind before use.
type TaskPatch struct {
	TaskID          string
	Owner           Owner
	ExpectedVersion uint64
	CronExpression  *string
	Payload         *[]byte
	Silent          *bool
	Enabled         *bool
}

type Repository struct {
	DB                      *sql.DB
	Protector               Protector
	Now                     func() time.Time
	NewID                   func() string
	NewTraceID              func() (string, error)
	MaxEnabledTasksPerOwner int
	MaxEnabledTasksPerApp   int
}

// DueClaim is the scheduler's observed task identity. A stale observation is
// rejected after the transaction lock rather than being allowed to dispatch.
type DueClaim struct {
	TaskID          string
	ObservedVersion uint64
	ObservedSlot    time.Time
	Lease           time.Duration
}

// ClaimedRun is an immutable execution snapshot. S06 is a personal-local
// deployment, so task payloads are intentionally plaintext and never re-read
// from the mutable task row after a run is claimed.
type ClaimedRun struct {
	ID, TraceID, TaskID, AppID, ChatGroupID, ClaimToken string
	TaskVersion                                         uint64
	Kind                                                TaskKind
	Silent                                              bool
	ScheduledFor, LeaseUntil                            time.Time
	Payload                                             []byte
}

func (r Repository) CreateTask(ctx context.Context, draft TaskDraft) (Task, error) {
	if r.DB == nil {
		return Task{}, fmt.Errorf("schedule store database is required")
	}
	return r.createTaskExec(ctx, r.DB, draft)
}

type scheduleExec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type scheduleQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// createTaskExec is shared by the ordinary repository entry point and the
// tool-call transaction. Keeping the INSERT on the supplied executor is what
// prevents a successful ledger result from being committed separately from
// the task row it describes.
func (r Repository) createTaskExec(ctx context.Context, executor scheduleExec, draft TaskDraft) (Task, error) {
	if strings.TrimSpace(draft.ID) == "" {
		return Task{}, fmt.Errorf("schedule task id is required")
	}
	if err := draft.Owner.Validate(); err != nil {
		return Task{}, err
	}
	if draft.Kind != TaskPrompt && draft.Kind != TaskScript {
		return Task{}, fmt.Errorf("schedule task kind is invalid")
	}
	if len(draft.Payload) == 0 {
		return Task{}, fmt.Errorf("schedule task payload is required")
	}
	cron, err := ParseCron(draft.CronExpression)
	if err != nil {
		return Task{}, err
	}
	ownerHMAC, err := r.Protector.OwnerHMAC(draft.Owner)
	if err != nil {
		return Task{}, fmt.Errorf("index schedule owner: %w", err)
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	nextRunAt := cron.Next(now)
	if nextRunAt.IsZero() {
		return Task{}, fmt.Errorf("compute schedule next run")
	}
	_, err = executor.ExecContext(ctx, `INSERT INTO scheduled_tasks (id,app_id,chat_group_id,creator_open_id_hmac,creator_open_id,kind,cron_expression,timezone,payload_text,silent,enabled,version,next_run_at) VALUES (?,?,?,?,?,?,?,?,?, ?,TRUE,1,?)`,
		draft.ID, draft.Owner.AppID, draft.Owner.ChatGroupID, ownerHMAC, draft.Owner.OpenID,
		string(draft.Kind), strings.TrimSpace(draft.CronExpression), cron.Timezone(), string(draft.Payload), draft.Silent, nextRunAt,
	)
	if err != nil {
		return Task{}, fmt.Errorf("insert scheduled task: %w", err)
	}
	return Task{ID: draft.ID, Kind: draft.Kind, CronExpression: strings.TrimSpace(draft.CronExpression), Timezone: cron.Timezone(), Silent: draft.Silent, Enabled: true, Version: 1, NextRunAt: nextRunAt}, nil
}

func (r Repository) checkCreateQuotaTx(ctx context.Context, tx *sql.Tx, owner Owner) error {
	if r.MaxEnabledTasksPerOwner <= 0 && r.MaxEnabledTasksPerApp <= 0 {
		return nil
	}
	if err := owner.Validate(); err != nil {
		return err
	}
	ownerHMAC, err := r.Protector.OwnerHMAC(owner)
	if err != nil {
		return fmt.Errorf("index schedule owner quota: %w", err)
	}
	if r.MaxEnabledTasksPerOwner > 0 {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduled_tasks WHERE app_id=? AND chat_group_id=? AND creator_open_id_hmac=? AND enabled=TRUE FOR UPDATE`, owner.AppID, owner.ChatGroupID, ownerHMAC).Scan(&count); err != nil {
			return fmt.Errorf("count owner schedule quota: %w", err)
		}
		if count >= r.MaxEnabledTasksPerOwner {
			return ErrTaskQuota
		}
	}
	if r.MaxEnabledTasksPerApp > 0 {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduled_tasks WHERE app_id=? AND enabled=TRUE FOR UPDATE`, owner.AppID).Scan(&count); err != nil {
			return fmt.Errorf("count app schedule quota: %w", err)
		}
		if count >= r.MaxEnabledTasksPerApp {
			return ErrTaskQuota
		}
	}
	return nil
}

func (r Repository) UpdateTask(ctx context.Context, patch TaskPatch) (Task, error) {
	if r.DB == nil {
		return Task{}, fmt.Errorf("schedule store database is required")
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, fmt.Errorf("begin schedule task update: %w", err)
	}
	defer tx.Rollback()
	task, err := r.updateTaskTx(ctx, tx, patch)
	if err != nil {
		return Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("commit schedule task update: %w", err)
	}
	return task, nil
}

func (r Repository) updateTaskTx(ctx context.Context, tx *sql.Tx, patch TaskPatch) (Task, error) {
	if strings.TrimSpace(patch.TaskID) == "" || patch.ExpectedVersion == 0 {
		return Task{}, fmt.Errorf("schedule task id and version are required")
	}
	if patch.CronExpression == nil && patch.Payload == nil && patch.Silent == nil && patch.Enabled == nil {
		return Task{}, fmt.Errorf("schedule task update is empty")
	}
	if patch.Payload != nil && len(*patch.Payload) == 0 {
		return Task{}, fmt.Errorf("schedule task payload is required")
	}
	if err := patch.Owner.Validate(); err != nil {
		return Task{}, err
	}
	ownerHMAC, err := r.Protector.OwnerHMAC(patch.Owner)
	if err != nil {
		return Task{}, fmt.Errorf("index schedule owner: %w", err)
	}
	var stored struct {
		kind           string
		cronExpression string
		silent         bool
		enabled        bool
		version        uint64
		nextRunAt      sql.NullTime
		payload        string
	}
	err = tx.QueryRowContext(ctx, `SELECT kind,cron_expression,silent,enabled,version,next_run_at,payload_text FROM scheduled_tasks WHERE id=? AND app_id=? AND chat_group_id=? AND creator_open_id_hmac=? FOR UPDATE`, patch.TaskID, patch.Owner.AppID, patch.Owner.ChatGroupID, ownerHMAC).Scan(
		&stored.kind, &stored.cronExpression, &stored.silent, &stored.enabled, &stored.version, &stored.nextRunAt, &stored.payload,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("load schedule task for update: %w", err)
	}
	if stored.version != patch.ExpectedVersion {
		return Task{}, ErrVersionConflict
	}
	if stored.kind != string(TaskPrompt) && stored.kind != string(TaskScript) {
		return Task{}, fmt.Errorf("stored schedule task kind is invalid")
	}
	cronExpression := stored.cronExpression
	if patch.CronExpression != nil {
		cronExpression = strings.TrimSpace(*patch.CronExpression)
	}
	cron, err := ParseCron(cronExpression)
	if err != nil {
		return Task{}, err
	}
	payload := patch.Payload
	if payload == nil {
		if strings.TrimSpace(stored.payload) == "" {
			return Task{}, fmt.Errorf("stored schedule payload is missing")
		}
		plaintext := []byte(stored.payload)
		payload = &plaintext
	}
	silent := stored.silent
	if patch.Silent != nil {
		silent = *patch.Silent
	}
	enabled := stored.enabled
	if patch.Enabled != nil {
		enabled = *patch.Enabled
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	nextRunAt := time.Time{}
	if enabled {
		if !stored.nextRunAt.Valid || !stored.enabled || patch.CronExpression != nil {
			nextRunAt = cron.Next(now)
		} else {
			nextRunAt = stored.nextRunAt.Time.UTC()
		}
	}
	nextVersion := stored.version + 1
	var nextRunAtValue any
	if !nextRunAt.IsZero() {
		nextRunAtValue = nextRunAt
	}
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_tasks SET cron_expression=?,payload_text=?,silent=?,enabled=?,version=?,next_run_at=? WHERE id=? AND app_id=? AND chat_group_id=? AND creator_open_id_hmac=? AND version=?`,
		cronExpression, string(*payload), silent, enabled, nextVersion, nextRunAtValue, patch.TaskID, patch.Owner.AppID, patch.Owner.ChatGroupID, ownerHMAC, stored.version,
	)
	if err != nil {
		return Task{}, fmt.Errorf("update scheduled task: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Task{}, fmt.Errorf("read scheduled task update result: %w", err)
	}
	if changed != 1 {
		return Task{}, ErrVersionConflict
	}
	return Task{ID: patch.TaskID, Kind: TaskKind(stored.kind), CronExpression: cronExpression, Timezone: cron.Timezone(), Silent: silent, Enabled: enabled, Version: nextVersion, NextRunAt: nextRunAt}, nil
}

func (r Repository) ListOwnedTasks(ctx context.Context, owner Owner, after *CursorPosition, pageSize int) (TaskPage, error) {
	if r.DB == nil {
		return TaskPage{}, fmt.Errorf("schedule store database is required")
	}
	return r.listOwnedTasksQuery(ctx, r.DB, owner, after, pageSize)
}

func (r Repository) listOwnedTasksQuery(ctx context.Context, queryer scheduleQuery, owner Owner, after *CursorPosition, pageSize int) (TaskPage, error) {
	if err := owner.Validate(); err != nil {
		return TaskPage{}, err
	}
	if pageSize < 1 || pageSize > 100 {
		return TaskPage{}, fmt.Errorf("schedule page size is invalid")
	}
	ownerHMAC, err := r.Protector.OwnerHMAC(owner)
	if err != nil {
		return TaskPage{}, fmt.Errorf("index schedule owner: %w", err)
	}
	query := `SELECT id,kind,cron_expression,timezone,silent,enabled,version,next_run_at,payload_text,updated_at FROM scheduled_tasks WHERE app_id=? AND chat_group_id=? AND creator_open_id_hmac=?`
	args := []any{owner.AppID, owner.ChatGroupID, ownerHMAC}
	if after != nil {
		if after.UpdatedAt.IsZero() || strings.TrimSpace(after.TaskID) == "" {
			return TaskPage{}, fmt.Errorf("schedule cursor position is invalid")
		}
		query += ` AND (updated_at > ? OR (updated_at = ? AND id > ?))`
		args = append(args, after.UpdatedAt.UTC(), after.UpdatedAt.UTC(), after.TaskID)
	}
	query += ` ORDER BY updated_at ASC,id ASC LIMIT ?`
	args = append(args, pageSize+1)
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return TaskPage{}, fmt.Errorf("list owner schedule tasks: %w", err)
	}
	defer rows.Close()
	var tasks []OwnedTask
	for rows.Next() {
		var row struct {
			id, kind, cronExpression, timezone string
			silent, enabled                    bool
			version                            uint64
			nextRunAt                          sql.NullTime
			payload                            string
			updatedAt                          time.Time
		}
		if err := rows.Scan(&row.id, &row.kind, &row.cronExpression, &row.timezone, &row.silent, &row.enabled, &row.version, &row.nextRunAt, &row.payload, &row.updatedAt); err != nil {
			return TaskPage{}, fmt.Errorf("scan owner schedule task: %w", err)
		}
		if row.kind != string(TaskPrompt) && row.kind != string(TaskScript) {
			return TaskPage{}, fmt.Errorf("stored schedule task kind is invalid")
		}
		if strings.TrimSpace(row.payload) == "" {
			return TaskPage{}, fmt.Errorf("owner schedule task payload is missing")
		}
		var nextRunAt time.Time
		if row.nextRunAt.Valid {
			nextRunAt = row.nextRunAt.Time.UTC()
		}
		tasks = append(tasks, OwnedTask{Task: Task{ID: row.id, Kind: TaskKind(row.kind), CronExpression: row.cronExpression, Timezone: row.timezone, Silent: row.silent, Enabled: row.enabled, Version: row.version, NextRunAt: nextRunAt}, Payload: row.payload, UpdatedAt: row.updatedAt.UTC()})
	}
	if err := rows.Err(); err != nil {
		return TaskPage{}, fmt.Errorf("iterate owner schedule tasks: %w", err)
	}
	page := TaskPage{Tasks: tasks}
	if len(tasks) > pageSize {
		page.Tasks = tasks[:pageSize]
		last := page.Tasks[len(page.Tasks)-1]
		page.Next = &CursorPosition{UpdatedAt: last.UpdatedAt, TaskID: last.ID}
	}
	return page, nil
}

// GetOwnedTask is the exact owner-scoped lookup used before a payload-kind
// update. It must not be replaced with an unscoped task-ID query: a caller
// must not be able to learn whether another owner's task exists.
func (r Repository) GetOwnedTask(ctx context.Context, owner Owner, taskID string) (OwnedTask, error) {
	if r.DB == nil {
		return OwnedTask{}, fmt.Errorf("schedule store database is required")
	}
	if err := owner.Validate(); err != nil || strings.TrimSpace(taskID) == "" {
		return OwnedTask{}, ErrTaskNotFound
	}
	ownerHMAC, err := r.Protector.OwnerHMAC(owner)
	if err != nil {
		return OwnedTask{}, fmt.Errorf("index schedule owner: %w", err)
	}
	var row struct {
		kind, cronExpression, timezone string
		silent, enabled                bool
		version                        uint64
		nextRunAt                      sql.NullTime
		payload                        string
		updatedAt                      time.Time
	}
	err = r.DB.QueryRowContext(ctx, `SELECT kind,cron_expression,timezone,silent,enabled,version,next_run_at,payload_text,updated_at FROM scheduled_tasks WHERE id=? AND app_id=? AND chat_group_id=? AND creator_open_id_hmac=?`, taskID, owner.AppID, owner.ChatGroupID, ownerHMAC).Scan(&row.kind, &row.cronExpression, &row.timezone, &row.silent, &row.enabled, &row.version, &row.nextRunAt, &row.payload, &row.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OwnedTask{}, ErrTaskNotFound
	}
	if err != nil {
		return OwnedTask{}, fmt.Errorf("get owner schedule task: %w", err)
	}
	if row.kind != string(TaskPrompt) && row.kind != string(TaskScript) {
		return OwnedTask{}, fmt.Errorf("stored schedule task kind is invalid")
	}
	if strings.TrimSpace(row.payload) == "" {
		return OwnedTask{}, fmt.Errorf("owner schedule task payload is missing")
	}
	var nextRunAt time.Time
	if row.nextRunAt.Valid {
		nextRunAt = row.nextRunAt.Time.UTC()
	}
	return OwnedTask{Task: Task{ID: taskID, Kind: TaskKind(row.kind), CronExpression: row.cronExpression, Timezone: row.timezone, Silent: row.silent, Enabled: row.enabled, Version: row.version, NextRunAt: nextRunAt}, Payload: row.payload, UpdatedAt: row.updatedAt.UTC()}, nil
}

func (r Repository) ClaimDue(ctx context.Context, claim DueClaim) (ClaimedRun, error) {
	if r.DB == nil {
		return ClaimedRun{}, fmt.Errorf("schedule store database is required")
	}
	if strings.TrimSpace(claim.TaskID) == "" || claim.ObservedVersion == 0 || claim.ObservedSlot.IsZero() {
		return ClaimedRun{}, fmt.Errorf("schedule due claim is incomplete")
	}
	if claim.Lease <= 0 {
		claim.Lease = 30 * time.Second
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	now = now.UTC()
	claim.ObservedSlot = claim.ObservedSlot.UTC()
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return ClaimedRun{}, fmt.Errorf("begin schedule due claim: %w", err)
	}
	defer tx.Rollback()
	var stored struct {
		appID, chatGroupID, ownerHMAC, kind, cronExpression string
		silent, enabled                                     bool
		version                                             uint64
		nextRunAt                                           sql.NullTime
		payload                                             string
		appEnabled, scheduleEnabled                         bool
	}
	err = tx.QueryRowContext(ctx, `SELECT t.app_id,t.chat_group_id,t.creator_open_id_hmac,t.kind,t.cron_expression,t.silent,t.enabled,t.version,t.next_run_at,t.payload_text,a.enabled,cg.schedule_enabled FROM scheduled_tasks t JOIN apps a ON a.id=t.app_id JOIN chat_groups cg ON cg.id=t.chat_group_id WHERE t.id=? FOR UPDATE`, claim.TaskID).Scan(
		&stored.appID, &stored.chatGroupID, &stored.ownerHMAC, &stored.kind, &stored.cronExpression, &stored.silent, &stored.enabled, &stored.version, &stored.nextRunAt, &stored.payload, &stored.appEnabled, &stored.scheduleEnabled,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimedRun{}, ErrDueClaimConflict
	}
	if err != nil {
		return ClaimedRun{}, fmt.Errorf("lock scheduled task due claim: %w", err)
	}
	if !stored.enabled || !stored.appEnabled || !stored.scheduleEnabled || stored.version != claim.ObservedVersion || !stored.nextRunAt.Valid || !stored.nextRunAt.Time.UTC().Equal(claim.ObservedSlot) {
		return ClaimedRun{}, ErrDueClaimConflict
	}
	if stored.kind != string(TaskPrompt) && stored.kind != string(TaskScript) {
		return ClaimedRun{}, fmt.Errorf("stored schedule task kind is invalid")
	}
	if strings.TrimSpace(stored.payload) == "" {
		return ClaimedRun{}, fmt.Errorf("claimed schedule payload is missing")
	}
	cron, err := ParseCron(stored.cronExpression)
	if err != nil {
		return ClaimedRun{}, fmt.Errorf("parse claimed schedule cron: %w", err)
	}
	nextRunAt := cron.Next(claim.ObservedSlot)
	if nextRunAt.IsZero() {
		return ClaimedRun{}, fmt.Errorf("compute claimed schedule next run")
	}
	newID := r.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	runID, claimToken := newID(), newID()
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(claimToken) == "" {
		return ClaimedRun{}, fmt.Errorf("generate schedule run identity")
	}
	traceID, err := r.newTraceID()
	if err != nil {
		return ClaimedRun{}, err
	}
	leaseUntil := now.Add(claim.Lease)
	_, err = tx.ExecContext(ctx, `INSERT INTO scheduled_task_runs (id,trace_id,task_id,scheduled_for,task_version,kind,silent,payload_text,script_definition_id,script_content_hmac,script_key_version,state,claim_token,lease_until) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		runID, traceID, claim.TaskID, claim.ObservedSlot, stored.version, stored.kind, stored.silent, stored.payload, nil, nil, nil, "claimed", claimToken, leaseUntil,
	)
	if err != nil {
		return ClaimedRun{}, fmt.Errorf("insert scheduled task run claim: %w", err)
	}
	updated, err := tx.ExecContext(ctx, `UPDATE scheduled_tasks SET next_run_at=?,last_run_at=? WHERE id=? AND version=? AND next_run_at=?`, nextRunAt, now, claim.TaskID, stored.version, claim.ObservedSlot)
	if err != nil {
		return ClaimedRun{}, fmt.Errorf("advance scheduled task after claim: %w", err)
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return ClaimedRun{}, fmt.Errorf("read scheduled task advance result: %w", err)
	}
	if changed != 1 {
		return ClaimedRun{}, ErrDueClaimConflict
	}
	if err := tx.Commit(); err != nil {
		return ClaimedRun{}, fmt.Errorf("commit schedule due claim: %w", err)
	}
	return ClaimedRun{ID: runID, TraceID: traceID, TaskID: claim.TaskID, AppID: stored.appID, ChatGroupID: stored.chatGroupID, ClaimToken: claimToken, TaskVersion: stored.version, Kind: TaskKind(stored.kind), Silent: stored.silent, ScheduledFor: claim.ObservedSlot, LeaseUntil: leaseUntil, Payload: []byte(stored.payload)}, nil
}

func (r Repository) newTraceID() (string, error) {
	if r.NewTraceID != nil {
		traceID, err := r.NewTraceID()
		if err != nil {
			return "", fmt.Errorf("generate scheduled trace id: %w", err)
		}
		if len(traceID) != 32 {
			return "", fmt.Errorf("generate scheduled trace id: must be 32 hex characters")
		}
		if _, err := hex.DecodeString(traceID); err != nil {
			return "", fmt.Errorf("generate scheduled trace id: must be 32 hex characters")
		}
		if traceID != strings.ToLower(traceID) {
			return "", fmt.Errorf("generate scheduled trace id: must be lowercase canonical hex")
		}
		return traceID, nil
	}
	var raw [16]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate scheduled trace id: %w", err)
	}
	return fmt.Sprintf("%x", raw[:]), nil
}

// SkipMisfire records exactly one skipped slot and advances directly to the
// next future Cron occurrence. It never creates a claim token or dispatchable
// run, so restart cannot turn downtime into a catch-up burst.
func (r Repository) SkipMisfire(ctx context.Context, due DueTask, now time.Time) error {
	if r.DB == nil {
		return fmt.Errorf("schedule store database is required")
	}
	if strings.TrimSpace(due.ID) == "" || due.Version == 0 || due.NextRunAt.IsZero() || now.IsZero() {
		return fmt.Errorf("schedule misfire is invalid")
	}
	now = now.UTC()
	slot := due.NextRunAt.UTC()
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schedule misfire: %w", err)
	}
	defer tx.Rollback()
	var stored struct {
		kind, cron                  string
		silent, enabled             bool
		version                     uint64
		next                        sql.NullTime
		payload                     string
		appEnabled, scheduleEnabled bool
	}
	err = tx.QueryRowContext(ctx, `SELECT t.kind,t.cron_expression,t.silent,t.enabled,t.version,t.next_run_at,t.payload_text,a.enabled,cg.schedule_enabled FROM scheduled_tasks t JOIN apps a ON a.id=t.app_id JOIN chat_groups cg ON cg.id=t.chat_group_id WHERE t.id=? FOR UPDATE`, due.ID).Scan(&stored.kind, &stored.cron, &stored.silent, &stored.enabled, &stored.version, &stored.next, &stored.payload, &stored.appEnabled, &stored.scheduleEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDueClaimConflict
	}
	if err != nil {
		return fmt.Errorf("lock schedule misfire: %w", err)
	}
	if !stored.enabled || !stored.appEnabled || !stored.scheduleEnabled || stored.version != due.Version || !stored.next.Valid || !stored.next.Time.UTC().Equal(slot) || stored.kind != string(due.Kind) {
		return ErrDueClaimConflict
	}
	cron, err := ParseCron(stored.cron)
	if err != nil {
		return fmt.Errorf("parse schedule misfire cron: %w", err)
	}
	next := cron.Next(now)
	if next.IsZero() {
		return fmt.Errorf("compute next schedule misfire slot")
	}
	newID := r.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	runID := newID()
	if strings.TrimSpace(runID) == "" {
		return fmt.Errorf("generate schedule misfire run id")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO scheduled_task_runs (id,task_id,scheduled_for,task_version,kind,silent,payload_text,state,error_code,completed_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, runID, due.ID, slot, stored.version, stored.kind, stored.silent, stored.payload, "skipped", "skipped_misfire", now)
	if err != nil {
		return fmt.Errorf("insert skipped schedule run: %w", err)
	}
	updated, err := tx.ExecContext(ctx, `UPDATE scheduled_tasks SET next_run_at=?,last_run_at=? WHERE id=? AND version=? AND next_run_at=?`, next, now, due.ID, stored.version, slot)
	if err != nil {
		return fmt.Errorf("advance skipped schedule task: %w", err)
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return fmt.Errorf("read skipped schedule task advance: %w", err)
	}
	if count != 1 {
		return ErrDueClaimConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schedule misfire: %w", err)
	}
	return nil
}

// FailRouteRevoked records the currently due slot when its App or chat route
// was disabled after task creation. It advances that slot without dispatching
// so reenabling the route cannot later execute stale work.
func (r Repository) FailRouteRevoked(ctx context.Context, due DueTask, now time.Time) error {
	if r.DB == nil {
		return fmt.Errorf("schedule store database is required")
	}
	if strings.TrimSpace(due.ID) == "" || due.Version == 0 || due.NextRunAt.IsZero() || now.IsZero() {
		return fmt.Errorf("schedule route revoke is invalid")
	}
	now = now.UTC()
	slot := due.NextRunAt.UTC()
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schedule route revoke: %w", err)
	}
	defer tx.Rollback()
	var stored struct {
		kind, cron                  string
		silent, enabled             bool
		version                     uint64
		next                        sql.NullTime
		payload                     string
		appEnabled, scheduleEnabled bool
	}
	err = tx.QueryRowContext(ctx, `SELECT t.kind,t.cron_expression,t.silent,t.enabled,t.version,t.next_run_at,t.payload_text,a.enabled,cg.schedule_enabled FROM scheduled_tasks t JOIN apps a ON a.id=t.app_id JOIN chat_groups cg ON cg.id=t.chat_group_id WHERE t.id=? FOR UPDATE`, due.ID).Scan(&stored.kind, &stored.cron, &stored.silent, &stored.enabled, &stored.version, &stored.next, &stored.payload, &stored.appEnabled, &stored.scheduleEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDueClaimConflict
	}
	if err != nil {
		return fmt.Errorf("lock schedule route revoke: %w", err)
	}
	if !stored.enabled || (stored.appEnabled && stored.scheduleEnabled) || stored.version != due.Version || !stored.next.Valid || !stored.next.Time.UTC().Equal(slot) || stored.kind != string(due.Kind) {
		return ErrDueClaimConflict
	}
	cron, err := ParseCron(stored.cron)
	if err != nil {
		return fmt.Errorf("parse schedule route revoke cron: %w", err)
	}
	next := cron.Next(now)
	if next.IsZero() {
		return fmt.Errorf("compute next schedule route revoke slot")
	}
	newID := r.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	runID := newID()
	if strings.TrimSpace(runID) == "" {
		return fmt.Errorf("generate schedule route revoke run id")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO scheduled_task_runs (id,task_id,scheduled_for,task_version,kind,silent,payload_text,state,error_code,completed_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, runID, due.ID, slot, stored.version, stored.kind, stored.silent, stored.payload, "failed", "failed_route_revoked", now)
	if err != nil {
		return fmt.Errorf("insert route revoked schedule run: %w", err)
	}
	updated, err := tx.ExecContext(ctx, `UPDATE scheduled_tasks SET next_run_at=?,last_run_at=? WHERE id=? AND version=? AND next_run_at=?`, next, now, due.ID, stored.version, slot)
	if err != nil {
		return fmt.Errorf("advance route revoked schedule task: %w", err)
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return fmt.Errorf("read route revoked task advance: %w", err)
	}
	if count != 1 {
		return ErrDueClaimConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schedule route revoke: %w", err)
	}
	return nil
}
