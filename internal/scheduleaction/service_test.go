package scheduleaction

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/codexapp"
	"github.com/kid0317/codex-workspace-bot/internal/schedule"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

func TestServiceRejectsAbsentActorBeforeReadingArguments(t *testing.T) {
	store := &fakeStore{}
	service := Service{Store: store}
	result, err := service.Execute(context.Background(), Route{AppID: "app-1", ChannelKey: "p2p:ou-1:app-1", ChatGroupID: "group-1"}, codexapp.ToolCall{Tool: "schedule.create", Arguments: json.RawMessage(`{"kind":"prompt","prompt":"remind me","cron_expression":"0 9 * * *","silent":false}`)})
	if err != nil || result.Success || resultText(result) != "schedule_owner_ambiguous" || store.createCalls != 0 {
		t.Fatalf("result=%#v err=%v createCalls=%d", result, err, store.createCalls)
	}
}

func TestServiceCreateDerivesOwnerOnlyFromRoute(t *testing.T) {
	store := &fakeStore{createTask: schedule.Task{ID: "task-1", Kind: schedule.TaskPrompt, CronExpression: "0 9 * * *", Timezone: "Asia/Shanghai", Enabled: true, Version: 1, NextRunAt: time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC)}}
	service := Service{Store: store}
	result, err := service.Execute(context.Background(), Route{AppID: "app-1", ChannelKey: "p2p:ou-1:app-1", ChatGroupID: "group-1", Actor: worker.ActorPrincipal{OpenID: "ou-1"}}, codexapp.ToolCall{Tool: "schedule.create", Arguments: json.RawMessage(`{"kind":"prompt","prompt":"remind me","cron_expression":"0 9 * * *","silent":false,"open_id":"attacker"}`)})
	if err != nil || result.Success || resultText(result) != "schedule_invalid_payload" || store.createCalls != 0 {
		t.Fatalf("result=%#v err=%v draft=%#v", result, err, store.createDraft)
	}

	result, err = service.Execute(context.Background(), Route{AppID: "app-1", ChannelKey: "p2p:ou-1:app-1", ChatGroupID: "group-1", Actor: worker.ActorPrincipal{OpenID: "ou-1"}}, codexapp.ToolCall{Tool: "schedule.create", Arguments: json.RawMessage(`{"kind":"prompt","prompt":"remind me","cron_expression":"0 9 * * *","silent":false}`)})
	if err != nil || !result.Success || store.createCalls != 1 || store.createDraft.Owner != (schedule.Owner{AppID: "app-1", ChatGroupID: "group-1", OpenID: "ou-1"}) || string(store.createDraft.Payload) != "remind me" {
		t.Fatalf("result=%#v err=%v draft=%#v", result, err, store.createDraft)
	}
}

func TestServiceCreateAcceptsDirectLocalScriptCommandWithoutRegistry(t *testing.T) {
	store := &fakeStore{createTask: schedule.Task{ID: "task-1", Kind: schedule.TaskScript, CronExpression: "22 9 * * *", Timezone: "Asia/Shanghai", Enabled: true, Version: 1}}
	service := Service{Store: store, ScriptsEnabled: true}
	route := Route{AppID: "app-1", ChannelKey: "p2p:ou-1:app-1", ChatGroupID: "group-1", Actor: worker.ActorPrincipal{OpenID: "ou-1"}}
	result, err := service.Execute(context.Background(), route, codexapp.ToolCall{Tool: "schedule.create", Arguments: json.RawMessage(`{"kind":"script","command":"python /root/aipm-codex/check_today_holiday.py","cron_expression":"22 9 * * *","silent":false}`)})
	if err != nil || !result.Success || store.createCalls != 1 || store.createDraft.Kind != schedule.TaskScript || string(store.createDraft.Payload) != "python /root/aipm-codex/check_today_holiday.py" {
		t.Fatalf("result=%#v err=%v draft=%#v", result, err, store.createDraft)
	}
}

func TestServiceCreateUsesBoundCallIdentityForAtomicLedger(t *testing.T) {
	store := &fakeStore{}
	atomic := &fakeAtomicCreate{result: schedule.ToolCallExecution{Payload: []byte(`{"id":"task-1","version":1}`), Success: true}}
	service := Service{Store: store, AtomicCreate: atomic, ArgumentsHMAC: func(raw []byte) (string, error) { return "hmac:" + string(raw), nil }, NewID: func() string { return "task-1" }}
	route := Route{AppID: "app-1", ChannelKey: "p2p:ou-1:app-1", ChatGroupID: "group-1", Actor: worker.ActorPrincipal{OpenID: "ou-1"}}
	call := codexapp.ToolCall{ThreadID: "thread-1", TurnID: "turn-1", CallID: "call-1", Tool: "schedule.create", Arguments: json.RawMessage(`{"kind":"prompt","prompt":"remind me","cron_expression":"0 9 * * *","silent":false}`)}
	result, err := service.Execute(context.Background(), route, call)
	if err != nil || !result.Success || resultText(result) != `{"id":"task-1","version":1}` {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	identity := atomic.input.Identity
	if identity.AppID != "app-1" || identity.ChannelKey != "p2p:ou-1:app-1" || identity.ChatGroupID != "group-1" || identity.ThreadID != "thread-1" || identity.TurnID != "turn-1" || identity.CallID != "call-1" || identity.Tool != "schedule.create" || atomic.input.ArgumentsHMAC != "hmac:"+string(call.Arguments) {
		t.Fatalf("atomic input=%#v", atomic.input)
	}
	if store.createCalls != 0 || atomic.draft.Owner.OpenID != "ou-1" || string(atomic.draft.Payload) != "remind me" {
		t.Fatalf("fallback calls=%d atomic draft=%#v", store.createCalls, atomic.draft)
	}
}

func TestServiceValidatesPromptAndCronBeforeCreate(t *testing.T) {
	store := &fakeStore{}
	service := Service{Store: store}
	route := Route{AppID: "app-1", ChannelKey: "p2p:ou-1:app-1", ChatGroupID: "group-1", Actor: worker.ActorPrincipal{OpenID: "ou-1"}}
	for _, test := range []struct{ arguments, code string }{
		{`{"kind":"prompt","prompt":"","cron_expression":"0 9 * * *","silent":false}`, "schedule_invalid_payload"},
		{`{"kind":"prompt","prompt":"ok","cron_expression":"0 9 * * * *","silent":false}`, "schedule_invalid_cron"},
		{`{"kind":"prompt","prompt":"ok","cron_expression":"0 9 * * *","silent":"false"}`, "schedule_invalid_payload"},
	} {
		result, err := service.Execute(context.Background(), route, codexapp.ToolCall{Tool: "schedule.create", Arguments: json.RawMessage(test.arguments)})
		if err != nil || result.Success || resultText(result) != test.code || store.createCalls != 0 {
			t.Fatalf("arguments=%s result=%#v err=%v", test.arguments, result, err)
		}
	}
}

func TestServiceListUsesOwnerBoundCursor(t *testing.T) {
	store := &fakeStore{page: schedule.TaskPage{Tasks: []schedule.OwnedTask{{Task: schedule.Task{ID: "task-1", Kind: schedule.TaskPrompt, CronExpression: "0 9 * * *", Timezone: "Asia/Shanghai", Enabled: true, Version: 1}, Payload: "secret", UpdatedAt: time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC)}}, Next: &schedule.CursorPosition{UpdatedAt: time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC), TaskID: "task-1"}}}
	codec := cursorCodec(t)
	service := Service{Store: store, Cursors: codec, OwnerHMAC: ownerHMAC}
	route := Route{AppID: "app-1", ChannelKey: "p2p:ou-1:app-1", ChatGroupID: "group-1", Actor: worker.ActorPrincipal{OpenID: "ou-1"}}
	result, err := service.Execute(context.Background(), route, codexapp.ToolCall{Tool: "schedule.list_own", Arguments: json.RawMessage(`{"page_size":1}`)})
	if err != nil || !result.Success || store.listOwner.OpenID != "ou-1" || store.listPageSize != 1 {
		t.Fatalf("result=%#v err=%v owner=%#v size=%d", result, err, store.listOwner, store.listPageSize)
	}
	var body struct {
		Tasks []struct {
			Prompt string `json:"prompt"`
		} `json:"tasks"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(resultText(result)), &body); err != nil || len(body.Tasks) != 1 || body.Tasks[0].Prompt != "secret" || body.NextCursor == "" {
		t.Fatalf("body=%q err=%v", resultText(result), err)
	}

	result, err = service.Execute(context.Background(), Route{AppID: "app-1", ChannelKey: "p2p:ou-2:app-1", ChatGroupID: "group-1", Actor: worker.ActorPrincipal{OpenID: "ou-2"}}, codexapp.ToolCall{Tool: "schedule.list_own", Arguments: json.RawMessage(`{"page_size":1,"cursor":` + mustJSON(t, body.NextCursor) + `}`)})
	if err != nil || result.Success || resultText(result) != "schedule_invalid_cursor" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestServiceUpdateRejectsPayloadForOtherTaskKind(t *testing.T) {
	store := &fakeStore{owned: schedule.OwnedTask{Task: schedule.Task{ID: "task-1", Kind: schedule.TaskPrompt, Version: 1}}}
	service := Service{Store: store, ScriptsEnabled: true}
	route := Route{AppID: "app-1", ChannelKey: "p2p:ou-1:app-1", ChatGroupID: "group-1", Actor: worker.ActorPrincipal{OpenID: "ou-1"}}
	result, err := service.Execute(context.Background(), route, codexapp.ToolCall{Tool: "schedule.update", Arguments: json.RawMessage(`{"task_id":"task-1","version":1,"command":"python /root/aipm-codex/check_today_holiday.py"}`)})
	if err != nil || result.Success || resultText(result) != "schedule_invalid_payload" || store.updateCalls != 0 {
		t.Fatalf("result=%#v err=%v updates=%d", result, err, store.updateCalls)
	}
}

func TestServiceUpdateAcceptsIDReturnedByListAlongsideScheduleChange(t *testing.T) {
	store := &fakeStore{owned: schedule.OwnedTask{Task: schedule.Task{ID: "task-1", Kind: schedule.TaskScript, Version: 1}}}
	service := Service{Store: store, ScriptsEnabled: true}
	route := Route{AppID: "app-1", ChannelKey: "p2p:ou-1:app-1", ChatGroupID: "group-1", Actor: worker.ActorPrincipal{OpenID: "ou-1"}}
	result, err := service.Execute(context.Background(), route, codexapp.ToolCall{Tool: "schedule.update", Arguments: json.RawMessage(`{"id":"task-1","version":1,"kind":"script","cron_expression":"9 10 * * *"}`)})
	if err != nil || !result.Success || store.updateCalls != 1 || store.updatePatch.CronExpression == nil || *store.updatePatch.CronExpression != "9 10 * * *" {
		t.Fatalf("result=%#v err=%v updates=%d patch=%#v", result, err, store.updateCalls, store.updatePatch)
	}
}

func TestServiceUpdateRejectsKindChangeEvenWhenScheduleChangeIsValid(t *testing.T) {
	store := &fakeStore{owned: schedule.OwnedTask{Task: schedule.Task{ID: "task-1", Kind: schedule.TaskScript, Version: 1}}}
	service := Service{Store: store, ScriptsEnabled: true}
	route := Route{AppID: "app-1", ChannelKey: "p2p:ou-1:app-1", ChatGroupID: "group-1", Actor: worker.ActorPrincipal{OpenID: "ou-1"}}
	result, err := service.Execute(context.Background(), route, codexapp.ToolCall{Tool: "schedule.update", Arguments: json.RawMessage(`{"task_id":"task-1","version":1,"kind":"prompt","cron_expression":"9 10 * * *"}`)})
	if err != nil || result.Success || resultText(result) != "schedule_invalid_payload" || store.updateCalls != 0 {
		t.Fatalf("result=%#v err=%v updates=%d", result, err, store.updateCalls)
	}
}

func TestInvalidSchedulePayloadLogContainsFieldNamesButNotUserValues(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	service := Service{Store: &fakeStore{}}
	route := Route{AppID: "app-1", ChannelKey: "p2p:ou-1:app-1", ChatGroupID: "group-1", Actor: worker.ActorPrincipal{OpenID: "ou-1"}}
	secret := "do-not-log-this-command"
	result, err := service.Execute(context.Background(), route, codexapp.ToolCall{Tool: "schedule.update", Arguments: json.RawMessage(`{"id":"task-1","version":1,"command":"` + secret + `","prompt":"also-invalid"}`)})
	if err != nil || result.Success || resultText(result) != "schedule_invalid_payload" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	output := logs.String()
	if !strings.Contains(output, "schedule_tool_invalid_payload") || !strings.Contains(output, "argument_fields") || strings.Contains(output, secret) || strings.Contains(output, "also-invalid") {
		t.Fatalf("log=%q", output)
	}
}

type fakeStore struct {
	createTask   schedule.Task
	createDraft  schedule.TaskDraft
	createCalls  int
	page         schedule.TaskPage
	listOwner    schedule.Owner
	listPageSize int
	owned        schedule.OwnedTask
	updateCalls  int
	updatePatch  schedule.TaskPatch
}

type fakeAtomicCreate struct {
	input  schedule.ToolCallInput
	draft  schedule.TaskDraft
	result schedule.ToolCallExecution
}

func (s *fakeAtomicCreate) CreateTaskForToolCall(_ context.Context, input schedule.ToolCallInput, draft schedule.TaskDraft, encode func(schedule.Task) ([]byte, error)) (schedule.ToolCallExecution, error) {
	s.input, s.draft = input, draft
	return s.result, nil
}

func (s *fakeStore) CreateTask(_ context.Context, draft schedule.TaskDraft) (schedule.Task, error) {
	s.createCalls++
	s.createDraft = draft
	return s.createTask, nil
}
func (s *fakeStore) ListOwnedTasks(_ context.Context, owner schedule.Owner, _ *schedule.CursorPosition, pageSize int) (schedule.TaskPage, error) {
	s.listOwner = owner
	s.listPageSize = pageSize
	return s.page, nil
}
func (s *fakeStore) UpdateTask(_ context.Context, patch schedule.TaskPatch) (schedule.Task, error) {
	s.updateCalls++
	s.updatePatch = patch
	return schedule.Task{}, nil
}
func (s *fakeStore) GetOwnedTask(context.Context, schedule.Owner, string) (schedule.OwnedTask, error) {
	return s.owned, nil
}

func cursorCodec(t *testing.T) schedule.CursorCodec {
	t.Helper()
	keys, err := schedule.NewKeyring([]schedule.Key{{Version: 1, Material: []byte("01234567890123456789012345678901")}})
	if err != nil {
		t.Fatal(err)
	}
	return schedule.CursorCodec{Keys: keys, Now: func() time.Time { return time.Date(2026, 7, 13, 2, 0, 0, 0, time.UTC) }}
}

func resultText(result codexapp.ToolResult) string {
	if len(result.ContentItems) == 0 {
		return ""
	}
	return result.ContentItems[0].Text
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func ownerHMAC(owner schedule.Owner) (string, error) { return "owner:" + owner.OpenID, nil }
