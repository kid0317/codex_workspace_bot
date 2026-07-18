// Package scheduleaction adapts an exact App Server tool call to the
// owner-scoped S06 schedule repository. It deliberately derives all route
// identity from the worker batch; tool JSON never contains identity fields.
package scheduleaction

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/kid0317/codex-workspace-bot/internal/codexapp"
	"github.com/kid0317/codex-workspace-bot/internal/schedule"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

const (
	toolList   = "schedule.list_own"
	toolCreate = "schedule.create"
	toolUpdate = "schedule.update"
)

type Route struct {
	AppID, ChannelKey, ChatGroupID string
	Actor                          worker.ActorPrincipal
}

type TaskStore interface {
	CreateTask(context.Context, schedule.TaskDraft) (schedule.Task, error)
	ListOwnedTasks(context.Context, schedule.Owner, *schedule.CursorPosition, int) (schedule.TaskPage, error)
	GetOwnedTask(context.Context, schedule.Owner, string) (schedule.OwnedTask, error)
	UpdateTask(context.Context, schedule.TaskPatch) (schedule.Task, error)
}

// AtomicCreateStore is the production mutation boundary: it owns both the
// task INSERT and the schedule tool-call ledger transaction. The adapter must
// not reconstruct this by separately calling CreateTask and ClaimToolCall.
type AtomicCreateStore interface {
	CreateTaskForToolCall(context.Context, schedule.ToolCallInput, schedule.TaskDraft, func(schedule.Task) ([]byte, error)) (schedule.ToolCallExecution, error)
}

type AtomicListStore interface {
	ListOwnedTasksForToolCall(context.Context, schedule.ToolCallInput, schedule.Owner, *schedule.CursorPosition, int, func(schedule.TaskPage) ([]byte, error)) (schedule.ToolCallExecution, error)
}

type AtomicUpdateStore interface {
	UpdateTaskForToolCall(context.Context, schedule.ToolCallInput, schedule.TaskPatch, func(schedule.Task) ([]byte, error)) (schedule.ToolCallExecution, error)
}

type Service struct {
	Store          TaskStore
	AtomicCreate   AtomicCreateStore
	AtomicList     AtomicListStore
	AtomicUpdate   AtomicUpdateStore
	Cursors        schedule.CursorCodec
	OwnerHMAC      func(schedule.Owner) (string, error)
	ArgumentsHMAC  func([]byte) (string, error)
	ScriptsEnabled bool
	NewID          func() string
}

func (s Service) Execute(ctx context.Context, route Route, call codexapp.ToolCall) (codexapp.ToolResult, error) {
	owner, err := route.owner()
	if err != nil {
		return toolFailure("schedule_owner_ambiguous"), nil
	}
	if s.Store == nil {
		return toolFailure("schedule_store_failed"), nil
	}
	switch canonicalTool(call.Tool) {
	case toolCreate:
		return s.create(ctx, route, owner, call)
	case toolList:
		return s.list(ctx, route, owner, call)
	case toolUpdate:
		return s.update(ctx, route, owner, call)
	default:
		return toolFailure("tool is unavailable"), nil
	}
}

func (r Route) owner() (schedule.Owner, error) {
	owner := schedule.Owner{AppID: r.AppID, ChatGroupID: r.ChatGroupID, OpenID: r.Actor.OpenID}
	return owner, owner.Validate()
}

func canonicalTool(tool string) string {
	switch tool {
	case "list_own":
		return toolList
	case "create":
		return toolCreate
	case "update":
		return toolUpdate
	default:
		return tool
	}
}

type createArgs struct {
	Kind           schedule.TaskKind `json:"kind"`
	Prompt         *string           `json:"prompt"`
	Command        *string           `json:"command"`
	CronExpression string            `json:"cron_expression"`
	Silent         *bool             `json:"silent"`
}

func (s Service) create(ctx context.Context, route Route, owner schedule.Owner, call codexapp.ToolCall) (codexapp.ToolResult, error) {
	raw := call.Arguments
	var args createArgs
	if !decode(raw, &args) || args.Silent == nil {
		logInvalidPayload(call, raw)
		return toolFailure("schedule_invalid_payload"), nil
	}
	if !validCron(args.CronExpression) {
		return toolFailure("schedule_invalid_cron"), nil
	}
	payload, code := s.createPayload(args)
	if code != "" {
		return toolFailure(code), nil
	}
	newID := s.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	draft := schedule.TaskDraft{ID: newID(), Owner: owner, Kind: args.Kind, CronExpression: strings.TrimSpace(args.CronExpression), Payload: payload, Silent: *args.Silent}
	if s.AtomicCreate != nil {
		argumentsHMAC, err := s.argumentsHMAC(raw)
		if err != nil {
			return toolFailure("schedule_store_failed"), nil
		}
		execution, err := s.AtomicCreate.CreateTaskForToolCall(ctx, toolCallInput(owner, route.ChannelKey, call, argumentsHMAC), draft, func(task schedule.Task) ([]byte, error) {
			return json.Marshal(taskResponse(task, ""))
		})
		if err != nil {
			return toolFailure(toolCallErrorCode(err)), nil
		}
		if !execution.Success {
			return toolFailure(execution.ErrorCode), nil
		}
		return toolSuccess(string(execution.Payload)), nil
	}
	task, err := s.Store.CreateTask(ctx, draft)
	if err != nil {
		return toolFailure(createErrorCode(err)), nil
	}
	return marshalSuccess(taskResponse(task, ""))
}

func (s Service) argumentsHMAC(raw []byte) (string, error) {
	if s.ArgumentsHMAC == nil {
		return "", errors.New("schedule arguments hmac is unavailable")
	}
	return s.ArgumentsHMAC(raw)
}

func toolCallInput(owner schedule.Owner, channelKey string, call codexapp.ToolCall, argumentsHMAC string) schedule.ToolCallInput {
	return schedule.ToolCallInput{Identity: schedule.ToolCallIdentity{AppID: owner.AppID, ChannelKey: channelKey, ChatGroupID: owner.ChatGroupID, ThreadID: call.ThreadID, TurnID: call.TurnID, CallID: call.CallID, Tool: canonicalTool(call.Tool)}, ArgumentsHMAC: argumentsHMAC}
}

func toolCallErrorCode(err error) string {
	switch {
	case errors.Is(err, schedule.ErrTaskQuota):
		return "schedule_quota_exceeded"
	case errors.Is(err, schedule.ErrToolCallConflict):
		return "schedule_tool_call_conflict"
	case errors.Is(err, schedule.ErrToolCallBusy):
		return "schedule_tool_call_busy"
	default:
		return "schedule_store_failed"
	}
}

func (s Service) createPayload(args createArgs) ([]byte, string) {
	switch args.Kind {
	case schedule.TaskPrompt:
		if args.Command != nil || args.Prompt == nil || !validPrompt(*args.Prompt) {
			return nil, "schedule_invalid_payload"
		}
		return []byte(*args.Prompt), ""
	case schedule.TaskScript:
		if args.Prompt != nil || args.Command == nil || !validCommand(*args.Command) {
			return nil, "schedule_invalid_payload"
		}
		if !s.ScriptsEnabled {
			return nil, "schedule_scripts_disabled"
		}
		return []byte(*args.Command), ""
	default:
		return nil, "schedule_invalid_payload"
	}
}

type listArgs struct {
	PageSize *int    `json:"page_size"`
	Cursor   *string `json:"cursor"`
}

func (s Service) list(ctx context.Context, route Route, owner schedule.Owner, call codexapp.ToolCall) (codexapp.ToolResult, error) {
	raw := call.Arguments
	var args listArgs
	if !decode(raw, &args) {
		return toolFailure("schedule_invalid_cursor"), nil
	}
	pageSize := 50
	if args.PageSize != nil {
		pageSize = *args.PageSize
	}
	if pageSize < 1 || pageSize > 100 {
		return toolFailure("schedule_invalid_cursor"), nil
	}
	var after *schedule.CursorPosition
	if args.Cursor != nil {
		if s.OwnerHMAC == nil {
			return toolFailure("schedule_list_failed"), nil
		}
		ownerHMAC, err := s.OwnerHMAC(owner)
		if err != nil {
			return toolFailure("schedule_list_failed"), nil
		}
		position, err := s.Cursors.Decode(ownerHMAC, *args.Cursor)
		if err != nil {
			return toolFailure("schedule_invalid_cursor"), nil
		}
		after = &position
	}
	if s.AtomicList != nil {
		argumentsHMAC, err := s.argumentsHMAC(raw)
		if err != nil {
			return toolFailure("schedule_list_failed"), nil
		}
		execution, err := s.AtomicList.ListOwnedTasksForToolCall(ctx, toolCallInput(owner, route.ChannelKey, call, argumentsHMAC), owner, after, pageSize, func(page schedule.TaskPage) ([]byte, error) {
			return s.encodeTaskPage(page, owner)
		})
		if err != nil {
			return toolFailure(toolCallErrorCode(err)), nil
		}
		if !execution.Success {
			return toolFailure(execution.ErrorCode), nil
		}
		return toolSuccess(string(execution.Payload)), nil
	}
	page, err := s.Store.ListOwnedTasks(ctx, owner, after, pageSize)
	if err != nil {
		return toolFailure("schedule_list_failed"), nil
	}
	encoded, err := s.encodeTaskPage(page, owner)
	if err != nil {
		return toolFailure("schedule_list_failed"), nil
	}
	return toolSuccess(string(encoded)), nil
}

func (s Service) encodeTaskPage(page schedule.TaskPage, owner schedule.Owner) ([]byte, error) {
	response := taskPageResponse{Tasks: make([]taskView, 0, len(page.Tasks))}
	for _, task := range page.Tasks {
		response.Tasks = append(response.Tasks, taskViewFor(task))
	}
	if page.Next != nil {
		if s.OwnerHMAC == nil {
			return nil, errors.New("schedule owner hmac is unavailable")
		}
		ownerHMAC, err := s.OwnerHMAC(owner)
		if err != nil {
			return nil, err
		}
		next, err := s.Cursors.Encode(ownerHMAC, *page.Next)
		if err != nil {
			return nil, err
		}
		response.NextCursor = next
	}
	return json.Marshal(response)
}

type updateArgs struct {
	ID             string             `json:"id"`
	Version        uint64             `json:"version"`
	Kind           *schedule.TaskKind `json:"kind"`
	CronExpression *string            `json:"cron_expression"`
	Silent         *bool              `json:"silent"`
	Enabled        *bool              `json:"enabled"`
	Prompt         *string            `json:"prompt"`
	Command        *string            `json:"command"`
}

func (s Service) update(ctx context.Context, route Route, owner schedule.Owner, call codexapp.ToolCall) (codexapp.ToolResult, error) {
	raw := call.Arguments
	var args updateArgs
	if !decode(raw, &args) || strings.TrimSpace(args.ID) == "" || args.Version == 0 || (args.CronExpression == nil && args.Silent == nil && args.Enabled == nil && args.Prompt == nil && args.Command == nil) || (args.Prompt != nil && args.Command != nil) {
		logInvalidPayload(call, raw)
		return toolFailure("schedule_invalid_payload"), nil
	}
	if args.CronExpression != nil && !validCron(*args.CronExpression) {
		return toolFailure("schedule_invalid_cron"), nil
	}
	var payload *[]byte
	if args.Prompt != nil || args.Command != nil || args.Kind != nil {
		current, err := s.Store.GetOwnedTask(ctx, owner, args.ID)
		if err == schedule.ErrTaskNotFound {
			return toolFailure("schedule_not_found"), nil
		}
		if err != nil {
			return toolFailure("schedule_store_failed"), nil
		}
		if (args.Kind != nil && *args.Kind != current.Kind) || (args.Prompt != nil && current.Kind != schedule.TaskPrompt) || (args.Command != nil && current.Kind != schedule.TaskScript) {
			logInvalidPayload(call, raw)
			return toolFailure("schedule_invalid_payload"), nil
		}
	}
	if args.Prompt != nil {
		if !validPrompt(*args.Prompt) {
			return toolFailure("schedule_invalid_payload"), nil
		}
		value := []byte(*args.Prompt)
		payload = &value
	}
	if args.Command != nil {
		if !validCommand(*args.Command) {
			return toolFailure("schedule_invalid_payload"), nil
		}
		if !s.ScriptsEnabled {
			return toolFailure("schedule_scripts_disabled"), nil
		}
		value := []byte(*args.Command)
		payload = &value
	}
	patch := schedule.TaskPatch{TaskID: args.ID, Owner: owner, ExpectedVersion: args.Version, CronExpression: args.CronExpression, Payload: payload, Silent: args.Silent, Enabled: args.Enabled}
	if s.AtomicUpdate != nil {
		argumentsHMAC, err := s.argumentsHMAC(raw)
		if err != nil {
			return toolFailure("schedule_store_failed"), nil
		}
		execution, err := s.AtomicUpdate.UpdateTaskForToolCall(ctx, toolCallInput(owner, route.ChannelKey, call, argumentsHMAC), patch, func(task schedule.Task) ([]byte, error) {
			return json.Marshal(taskResponse(task, "next_unclaimed_slot"))
		})
		if err != nil {
			return toolFailure(updateErrorCode(err)), nil
		}
		if !execution.Success {
			return toolFailure(execution.ErrorCode), nil
		}
		return toolSuccess(string(execution.Payload)), nil
	}
	task, err := s.Store.UpdateTask(ctx, patch)
	if err == schedule.ErrTaskNotFound {
		return toolFailure("schedule_not_found"), nil
	}
	if err == schedule.ErrVersionConflict {
		return toolFailure("schedule_version_conflict"), nil
	}
	if err != nil {
		return toolFailure("schedule_store_failed"), nil
	}
	return marshalSuccess(taskResponse(task, "next_unclaimed_slot"))
}

// logInvalidPayload provides diagnosis without recording user instructions or
// direct local commands. Field names are sufficient to distinguish an outdated
// dynamic-tool schema from a malformed invocation.
func logInvalidPayload(call codexapp.ToolCall, raw json.RawMessage) {
	fields := make(map[string]json.RawMessage)
	_ = json.Unmarshal(raw, &fields)
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	slog.Info("schedule tool rejected invalid payload",
		"event", "schedule_tool_invalid_payload",
		"tool", canonicalTool(call.Tool),
		"argument_fields", names,
		"argument_bytes", len(raw),
	)
}

func updateErrorCode(err error) string {
	switch {
	case errors.Is(err, schedule.ErrTaskNotFound):
		return "schedule_not_found"
	case errors.Is(err, schedule.ErrVersionConflict):
		return "schedule_version_conflict"
	default:
		return toolCallErrorCode(err)
	}
}

func validCron(value string) bool { _, err := schedule.ParseCron(value); return err == nil }
func validPrompt(value string) bool {
	return utf8.ValidString(value) && len(value) >= 1 && len(value) <= 16*1024
}
func validCommand(value string) bool {
	return utf8.ValidString(value) && len(strings.TrimSpace(value)) >= 1 && len(value) <= 16*1024
}
func createErrorCode(err error) string {
	if errors.Is(err, schedule.ErrTaskQuota) {
		return "schedule_quota_exceeded"
	}
	if strings.Contains(err.Error(), "cron") {
		return "schedule_invalid_cron"
	}
	return "schedule_store_failed"
}

type taskView struct {
	ID             string            `json:"id"`
	Kind           schedule.TaskKind `json:"kind"`
	Prompt         string            `json:"prompt,omitempty"`
	Command        string            `json:"command,omitempty"`
	CronExpression string            `json:"cron_expression"`
	Timezone       string            `json:"timezone"`
	Silent         bool              `json:"silent"`
	Enabled        bool              `json:"enabled"`
	Version        uint64            `json:"version"`
	NextRunAt      string            `json:"next_run_at,omitempty"`
}
type taskPageResponse struct {
	Tasks      []taskView `json:"tasks"`
	NextCursor string     `json:"next_cursor,omitempty"`
}
type taskMutationResponse struct {
	taskView
	EffectiveFrom string `json:"effective_from,omitempty"`
}

func taskViewFor(task schedule.OwnedTask) taskView {
	view := taskView{ID: task.ID, Kind: task.Kind, CronExpression: task.CronExpression, Timezone: task.Timezone, Silent: task.Silent, Enabled: task.Enabled, Version: task.Version}
	if task.Kind == schedule.TaskPrompt {
		view.Prompt = task.Payload
	} else {
		view.Command = task.Payload
	}
	if !task.NextRunAt.IsZero() {
		view.NextRunAt = task.NextRunAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return view
}
func taskResponse(task schedule.Task, effective string) taskMutationResponse {
	view := taskView{ID: task.ID, Kind: task.Kind, CronExpression: task.CronExpression, Timezone: task.Timezone, Silent: task.Silent, Enabled: task.Enabled, Version: task.Version}
	if !task.NextRunAt.IsZero() {
		view.NextRunAt = task.NextRunAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return taskMutationResponse{taskView: view, EffectiveFrom: effective}
}
func decode(raw json.RawMessage, destination any) bool {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination) == nil && decoder.Decode(&struct{}{}) != nil
}
func marshalSuccess(value any) (codexapp.ToolResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return toolFailure("schedule_store_failed"), nil
	}
	return toolSuccess(string(encoded)), nil
}
func toolSuccess(text string) codexapp.ToolResult {
	return codexapp.ToolResult{Success: true, ContentItems: []codexapp.ToolContentItem{{Type: "inputText", Text: text}}}
}
func toolFailure(text string) codexapp.ToolResult {
	return codexapp.ToolResult{Success: false, ContentItems: []codexapp.ToolContentItem{{Type: "inputText", Text: text}}}
}
