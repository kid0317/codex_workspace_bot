package codexapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kid0317/codex-workspace-bot/internal/catalog"
	"github.com/kid0317/codex-workspace-bot/internal/observability"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

type ThreadStore interface {
	GetChatGroupThread(context.Context, string) (string, error)
	SetThreadIfExpected(context.Context, string, string, string) (bool, error)
	GetChatGroupToolset(context.Context, string) (string, error)
	SetChatGroupToolset(context.Context, string, string) error
}

// CatalogUpgradeStore is optional only while legacy unit fakes are migrated.
// The production MySQL store implements it; S06 never resumes a thread unless
// its persisted catalog state is stable at the requested version.
type CatalogUpgradeStore interface {
	PrepareCatalogUpgrade(context.Context, string, string) (catalog.Upgrade, error)
	AdvanceCatalogUpgradeAfterArchive(context.Context, string, string) (bool, error)
	CompleteCatalogUpgrade(context.Context, string, string, string) (bool, error)
}

// TurnUsageLedger is implemented by MySQL storage. It is deliberately called
// after Runtime has returned to the channel worker, never from the App Server
// stdout reader, so database latency cannot stall protocol dispatch.
type TurnUsageLedger interface {
	RecordTurnUsage(context.Context, observability.TurnUsageRecord) (observability.SessionUsageTotal, bool, error)
	RecordThreadUsageSnapshot(context.Context, observability.ThreadUsageSnapshotRecord) (observability.SessionUsageTotal, error)
}
type TextInput struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
	// ObservationPath is local-only and never sent to App Server. It lets S08
	// export ordinary file bytes just as it exports localImage bytes.
	ObservationPath string `json:"-"`
}
type ThreadStartParams struct {
	CWD            string        `json:"cwd"`
	Model          string        `json:"model,omitempty"`
	ApprovalPolicy string        `json:"approvalPolicy"`
	Sandbox        string        `json:"sandbox"`
	DynamicTools   []DynamicTool `json:"dynamicTools,omitempty"`
}

type DynamicTool struct {
	Name        string          `json:"name"`
	Namespace   string          `json:"namespace"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

const (
	FeishuToolMessageSendCurrentChannel       = "feishu.message_send_current_channel"
	FeishuToolFileUploadAndSendCurrentChannel = "feishu.file_upload_and_send_current_channel"
	FeishuToolDocCreateAndAnnounce            = "feishu.doc_create_and_announce"
	FeishuToolDocRead                         = "feishu.doc_read"
	// Bump this whenever an S06 dynamic-tool schema changes. Existing App
	// Server Threads retain their original tool definitions on resume, so the
	// version is the durable invalidation key that forces archive/start.
	S06ToolCatalogVersion = "s06-schedule-v4"
	feishuToolsetVersion  = "s05-feishu-v2"
)

type ThreadResumeParams struct {
	ThreadID       string `json:"threadId"`
	CWD            string `json:"cwd"`
	Model          string `json:"model,omitempty"`
	ApprovalPolicy string `json:"approvalPolicy"`
	Sandbox        string `json:"sandbox"`
}
type TurnStartParams struct {
	ThreadID            string                   `json:"threadId"`
	CWD                 string                   `json:"cwd"`
	Model               string                   `json:"model,omitempty"`
	Effort              string                   `json:"effort,omitempty"`
	ApprovalPolicy      string                   `json:"approvalPolicy"`
	ClientUserMessageID string                   `json:"clientUserMessageId"`
	Input               []TextInput              `json:"input"`
	Route               RouteMetadata            `json:"-"`
	OnTurnStarted       func(string)             `json:"-"`
	OnTurnCompleted     func(TurnCompleted)      `json:"-"`
	Observation         *observability.Attempt   `json:"-"`
	OnItem              func(CompletedItem) bool `json:"-"`
	ToolHandler         ToolHandler              `json:"-"`
}

// TurnCompleted carries authoritative usage when present. SnapshotUsage is a
// separate exact cumulative Thread fallback for App Server versions that omit
// usage in turn/completed; it is never written as a synthetic turn ledger.
type TurnCompleted struct {
	ThreadID, TurnID, Status string
	Usage                    *observability.Usage
	SnapshotUsage            *observability.Usage
}

type ToolCall struct {
	ThreadID  string          `json:"threadId"`
	TurnID    string          `json:"turnId"`
	CallID    string          `json:"callId"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ToolResult struct {
	Success      bool              `json:"success"`
	ContentItems []ToolContentItem `json:"contentItems"`
}

type ToolHandler func(context.Context, ToolCall) (ToolResult, error)

type CompletedItem struct {
	ID, Type, Phase, Text string
}

type Processor struct {
	Runtime            *Runtime
	Store              ThreadStore
	Attachments        AttachmentResolver
	ToolHandlers       func(worker.Batch) ToolHandler
	ToolCatalogVersion string
	UsageLedger        TurnUsageLedger
	Observability      *observability.Recorder
}

// AttachmentResolver runs on the channel worker's processing path and returns
// only local, already-materialized inputs. It never exposes a Feishu resource
// key to Codex or to the App Server protocol.
type AttachmentResolver interface {
	Prepare(context.Context, worker.Batch) ([]TextInput, error)
}

// NewSession archives the current backing thread and clears the persisted
// pointer. The next ordinary message starts a new thread lazily.
func (p Processor) NewSession(ctx context.Context, chatGroupID string) error {
	threadID, err := p.Store.GetChatGroupThread(ctx, chatGroupID)
	if err != nil {
		return err
	}
	if threadID == "" {
		return nil
	}
	if _, err := p.Runtime.Call(ctx, "thread/archive", map[string]string{"threadId": threadID}); err != nil {
		return fmt.Errorf("archive current thread: %w", err)
	}
	cleared, err := p.Store.SetThreadIfExpected(ctx, chatGroupID, threadID, "")
	if err != nil {
		return err
	}
	if !cleared {
		return fmt.Errorf("clear current thread: changed concurrently")
	}
	return nil
}

func (p Processor) Process(ctx context.Context, batch worker.Batch) (worker.ProcessResult, error) {
	if p.Runtime.Availability() != Ready {
		return worker.ProcessResult{}, ErrUnavailable
	}
	if len(batch.Messages) == 0 || batch.Messages[0].ChatGroupID == "" {
		return worker.ProcessResult{}, fmt.Errorf("batch missing chat group")
	}
	groupID := batch.Messages[0].ChatGroupID
	if batch.Goal {
		return p.processGoal(ctx, batch, groupID)
	}
	threadID, err := p.Store.GetChatGroupThread(ctx, groupID)
	if err != nil {
		return worker.ProcessResult{}, err
	}
	startCatalogUpgrade := false
	if p.ToolHandlers != nil {
		threadID, startCatalogUpgrade, err = p.ensureToolCatalog(ctx, groupID, threadID)
		if err != nil {
			return worker.ProcessResult{}, err
		}
	}
	if threadID != "" && !startCatalogUpgrade {
		if _, err := p.Runtime.Call(ctx, "thread/resume", ThreadResumeParams{ThreadID: threadID, CWD: batch.Runtime.WorkspaceDir, Model: batch.Runtime.Model, ApprovalPolicy: "never", Sandbox: "danger-full-access"}); err == nil {
			return p.startTurn(ctx, batch, threadID)
		}
		slog.Info("resume_fallback_started_new_thread", "event", "resume_fallback_started_new_thread", "chat_group_id", groupID, "thread_id", threadID)
	}
	threadStart := ThreadStartParams{CWD: batch.Runtime.WorkspaceDir, Model: batch.Runtime.Model, ApprovalPolicy: "never", Sandbox: "danger-full-access"}
	if p.ToolHandlers != nil {
		threadStart.DynamicTools = p.dynamicTools()
	}
	result, err := p.Runtime.Call(ctx, "thread/start", threadStart)
	if err != nil {
		return worker.ProcessResult{}, err
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &response); err != nil || response.Thread.ID == "" {
		return worker.ProcessResult{}, fmt.Errorf("thread/start missing thread id")
	}
	changed := false
	if startCatalogUpgrade {
		upgrades, ok := p.Store.(CatalogUpgradeStore)
		if !ok {
			return worker.ProcessResult{}, fmt.Errorf("catalog upgrade store is required")
		}
		changed, err = upgrades.CompleteCatalogUpgrade(ctx, groupID, p.targetToolCatalogVersion(), response.Thread.ID)
	} else {
		changed, err = p.Store.SetThreadIfExpected(ctx, groupID, threadID, response.Thread.ID)
	}
	if err != nil {
		return worker.ProcessResult{}, err
	}
	if !changed {
		_, _ = p.Runtime.Call(ctx, "thread/archive", map[string]string{"threadId": response.Thread.ID})
		_, _ = p.Store.GetChatGroupThread(ctx, groupID)
		return worker.ProcessResult{}, fmt.Errorf("chat group thread changed concurrently")
	}
	return p.startTurn(ctx, batch, response.Thread.ID)
}

func (p Processor) targetToolCatalogVersion() string {
	if p.ToolCatalogVersion == S06ToolCatalogVersion {
		return S06ToolCatalogVersion
	}
	return feishuToolsetVersion
}

func (p Processor) dynamicTools() []DynamicTool {
	if p.targetToolCatalogVersion() == S06ToolCatalogVersion {
		return S06DynamicTools()
	}
	return FeishuDynamicTools()
}

func (p Processor) ensureToolCatalog(ctx context.Context, groupID, threadID string) (string, bool, error) {
	target := p.targetToolCatalogVersion()
	if upgrades, ok := p.Store.(CatalogUpgradeStore); ok {
		state, err := upgrades.PrepareCatalogUpgrade(ctx, groupID, target)
		if err != nil {
			return "", false, err
		}
		switch state.State {
		case catalog.Stable:
			if state.CurrentVersion != target {
				return "", false, fmt.Errorf("catalog upgrade did not persist target")
			}
			return state.CurrentThreadID, false, nil
		case catalog.ArchivePending:
			if state.FromThreadID != "" {
				if _, err := p.Runtime.Call(ctx, "thread/archive", map[string]string{"threadId": state.FromThreadID}); err != nil {
					return "", false, fmt.Errorf("archive outdated tool catalog: %w", err)
				}
			}
			advanced, err := upgrades.AdvanceCatalogUpgradeAfterArchive(ctx, groupID, state.FromThreadID)
			if err != nil {
				return "", false, err
			}
			if !advanced {
				return "", false, fmt.Errorf("catalog archive transition changed concurrently")
			}
			return "", true, nil
		case catalog.StartPending:
			return "", true, nil
		default:
			return "", false, fmt.Errorf("catalog upgrade state is invalid")
		}
	}

	// Temporary compatibility for the existing S05-only test stores. Production
	// storage always follows the persisted state-machine branch above.
	toolset, err := p.Store.GetChatGroupToolset(ctx, groupID)
	if err != nil {
		return "", false, err
	}
	if toolset == target {
		return threadID, false, nil
	}
	if threadID != "" {
		if _, err := p.Runtime.Call(ctx, "thread/archive", map[string]string{"threadId": threadID}); err != nil {
			return "", false, fmt.Errorf("archive outdated tool catalog: %w", err)
		}
		cleared, err := p.Store.SetThreadIfExpected(ctx, groupID, threadID, "")
		if err != nil || !cleared {
			return "", false, fmt.Errorf("clear outdated tool catalog: %w", err)
		}
	}
	if err := p.Store.SetChatGroupToolset(ctx, groupID, target); err != nil {
		return "", false, err
	}
	return "", false, nil
}

func (p Processor) processGoal(ctx context.Context, batch worker.Batch, groupID string) (worker.ProcessResult, error) {
	objective := batch.Messages[0].Query
	if objective == "" {
		return worker.ProcessResult{}, fmt.Errorf("goal objective is required")
	}
	threadID, err := p.Store.GetChatGroupThread(ctx, groupID)
	if err != nil {
		return worker.ProcessResult{}, err
	}
	if threadID != "" {
		if _, err := p.Runtime.Call(ctx, "thread/resume", ThreadResumeParams{ThreadID: threadID, CWD: batch.Runtime.WorkspaceDir, Model: batch.Runtime.Model, ApprovalPolicy: "never", Sandbox: "danger-full-access"}); err == nil {
			return p.startGoal(ctx, batch, threadID, objective)
		}
		slog.Info("resume_fallback_started_new_thread", "event", "resume_fallback_started_new_thread", "chat_group_id", groupID, "thread_id", threadID)
	}
	threadStart := ThreadStartParams{CWD: batch.Runtime.WorkspaceDir, Model: batch.Runtime.Model, ApprovalPolicy: "never", Sandbox: "danger-full-access"}
	if p.ToolHandlers != nil {
		threadStart.DynamicTools = FeishuDynamicTools()
	}
	result, err := p.Runtime.Call(ctx, "thread/start", threadStart)
	if err != nil {
		return worker.ProcessResult{}, err
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &response); err != nil || response.Thread.ID == "" {
		return worker.ProcessResult{}, fmt.Errorf("thread/start missing thread id")
	}
	changed, err := p.Store.SetThreadIfExpected(ctx, groupID, threadID, response.Thread.ID)
	if err != nil {
		return worker.ProcessResult{}, err
	}
	if !changed {
		_, _ = p.Runtime.Call(ctx, "thread/archive", map[string]string{"threadId": response.Thread.ID})
		return worker.ProcessResult{}, fmt.Errorf("chat group thread changed concurrently")
	}
	return p.startGoal(ctx, batch, response.Thread.ID, objective)
}

func (p Processor) startGoal(ctx context.Context, batch worker.Batch, threadID, objective string) (worker.ProcessResult, error) {
	p.Runtime.RegisterGoalThread(threadID)
	attemptID := batch.ID
	if attemptID == "" {
		attemptID = uuid.NewString()
	}
	var turnID string
	var completedMu sync.Mutex
	var completedTurns []TurnCompleted
	var toolHandler ToolHandler
	if p.ToolHandlers != nil {
		toolHandler = p.ToolHandlers(batch)
	}
	input := []TextInput{{Type: "text", Text: objective}}
	observation := p.startObservation(batch, input)
	goal, err := p.Runtime.PrepareGoal(ctx, threadID, TurnStartParams{CWD: batch.Runtime.WorkspaceDir, Model: batch.Runtime.Model, Effort: batch.Runtime.Effort, ApprovalPolicy: "never", ClientUserMessageID: attemptID, Input: input, Route: RouteMetadata{AppID: batch.Runtime.ID, ChannelKey: batch.Key.String(), ChatGroupID: batch.Messages[0].ChatGroupID, AttemptID: attemptID}, ToolHandler: toolHandler, Observation: observation, OnTurnStarted: func(id string) { turnID = id }, OnTurnCompleted: func(completed TurnCompleted) {
		completedMu.Lock()
		completedTurns = append(completedTurns, completed)
		completedMu.Unlock()
	}, OnItem: func(item CompletedItem) bool {
		if batch.OnItem == nil {
			return true
		}
		return batch.OnItem(worker.PresentationItem{ID: item.ID, Type: item.Type, Phase: item.Phase, Text: item.Text})
	}})
	if err != nil {
		if observation != nil {
			observation.End(nil, err)
		}
		return worker.ProcessResult{}, err
	}
	defer goal.Close()
	if _, err := p.Runtime.Call(ctx, "thread/goal/set", map[string]any{"threadId": threadID, "objective": objective, "status": "active"}); err != nil {
		return worker.ProcessResult{}, fmt.Errorf("set thread goal: %w", err)
	}
	startedAt, err := goal.Start(ctx)
	if err != nil {
		_ = goal.BeginStop(context.Background())
	}
	completedMu.Lock()
	turns := append([]TurnCompleted(nil), completedTurns...)
	completedMu.Unlock()
	anyMissingUsage, usedSnapshotFallback := len(turns) == 0, false
	for _, completed := range turns {
		if completed.Usage == nil && completed.SnapshotUsage == nil {
			anyMissingUsage = true
			break
		}
		if completed.Usage == nil && completed.SnapshotUsage != nil {
			usedSnapshotFallback = true
		}
	}
	if sessionTotal, usageErr := p.recordCompletedUsage(ctx, batch, turns); usageErr != nil {
		if err == nil {
			err = usageErr
		}
	} else if observation != nil {
		observation.SetSessionUsage(sessionTotal)
		if anyMissingUsage {
			observation.MarkSessionUsageIncomplete()
		}
		if usedSnapshotFallback {
			observation.MarkSessionUsageSnapshotFallback()
		}
	}
	if observation != nil {
		observation.End(nil, err)
	}
	return worker.ProcessResult{DurationMS: durationMilliseconds(startedAt, p.Runtime.cfg.Now()), ThreadID: threadID, TurnID: turnID}, err
}
func (p Processor) startTurn(ctx context.Context, batch worker.Batch, threadID string) (worker.ProcessResult, error) {
	input := make([]TextInput, 0, len(batch.Messages)+1)
	if text := FormatNormalMessages(batch.Messages); text != "" {
		input = append(input, TextInput{Type: "text", Text: text})
	}
	if batch.Messages[0].HasRequiredAttachment {
		if p.Attachments == nil {
			return worker.ProcessResult{}, fmt.Errorf("attachment resolver is unavailable")
		}
		attachments, err := p.Attachments.Prepare(ctx, batch)
		if err != nil {
			return worker.ProcessResult{}, fmt.Errorf("prepare attachments: %w", err)
		}
		input = append(input, attachments...)
	}
	attemptID := uuid.NewString()
	var turnID string
	var completedMu sync.Mutex
	var completedTurns []TurnCompleted
	var scheduledFinal, scheduledCandidate string
	var toolHandler ToolHandler
	if p.ToolHandlers != nil {
		toolHandler = p.ToolHandlers(batch)
	}
	observation := p.startObservation(batch, input)
	startedAt, err := p.Runtime.StartTurn(ctx, threadID, TurnStartParams{CWD: batch.Runtime.WorkspaceDir, Model: batch.Runtime.Model, Effort: batch.Runtime.Effort, ApprovalPolicy: "never", ClientUserMessageID: attemptID, Input: input, Route: RouteMetadata{AppID: batch.Runtime.ID, ChannelKey: batch.Key.String(), ChatGroupID: batch.Messages[0].ChatGroupID, AttemptID: attemptID}, ToolHandler: toolHandler, Observation: observation, OnTurnStarted: func(id string) { turnID = id }, OnTurnCompleted: func(completed TurnCompleted) {
		completedMu.Lock()
		completedTurns = append(completedTurns, completed)
		completedMu.Unlock()
	}, OnItem: func(item CompletedItem) bool {
		if item.Type == "agentMessage" && strings.TrimSpace(item.Text) != "" {
			scheduledCandidate = item.Text
			if item.Phase == "final_answer" {
				scheduledFinal = item.Text
			}
			if batch.ScheduledTaskRunID != "" {
				return true
			}
		}
		if batch.OnItem == nil {
			return true
		}
		return batch.OnItem(worker.PresentationItem{ID: item.ID, Type: item.Type, Phase: item.Phase, Text: item.Text})
	}})
	if scheduledFinal == "" {
		scheduledFinal = scheduledCandidate
	}
	completedMu.Lock()
	turns := append([]TurnCompleted(nil), completedTurns...)
	completedMu.Unlock()
	anyMissingUsage, usedSnapshotFallback := len(turns) == 0, false
	for _, completed := range turns {
		if completed.Usage == nil && completed.SnapshotUsage == nil {
			anyMissingUsage = true
			break
		}
		if completed.Usage == nil && completed.SnapshotUsage != nil {
			usedSnapshotFallback = true
		}
	}
	if sessionTotal, usageErr := p.recordCompletedUsage(ctx, batch, turns); usageErr != nil {
		if err == nil {
			err = usageErr
		}
	} else if observation != nil {
		observation.SetSessionUsage(sessionTotal)
		if anyMissingUsage {
			observation.MarkSessionUsageIncomplete()
		}
		if usedSnapshotFallback {
			observation.MarkSessionUsageSnapshotFallback()
		}
	}
	if observation != nil {
		observation.End(scheduledFinal, err)
	}
	return worker.ProcessResult{DurationMS: durationMilliseconds(startedAt, p.Runtime.cfg.Now()), ThreadID: threadID, TurnID: turnID, FinalText: scheduledFinal}, err
}

func (p Processor) startObservation(batch worker.Batch, input []TextInput) *observability.Attempt {
	if p.Observability == nil || len(batch.Messages) == 0 || batch.Messages[0].TraceID == "" {
		return nil
	}
	first := batch.Messages[0]
	sourceIDs := make([]string, 0, len(batch.Messages))
	for _, message := range batch.Messages {
		sourceIDs = append(sourceIDs, message.ID)
	}
	metadata := map[string]string{
		"app_id":                  batch.Runtime.ID,
		"workspace_mode":          batch.Runtime.WorkspaceMode,
		"channel_key":             batch.Key.String(),
		"chat_type":               first.ChatType,
		"chat_id":                 first.ChatID,
		"user_open_id":            first.Actor.OpenID,
		"model_requested":         batch.Runtime.Model,
		"effort_requested":        batch.Runtime.Effort,
		"runtime_version":         "s08",
		"source_message_count":    fmt.Sprintf("%d", len(batch.Messages)),
		"source_first_message_id": first.ID,
		"source_message_ids":      strings.Join(sourceIDs, ","),
	}
	if batch.ScheduledTaskRunID != "" {
		metadata["task_type"] = "scheduled"
		metadata["scheduled_task_id"] = batch.ScheduledTaskID
	} else {
		metadata["task_type"] = "realtime"
	}
	sessionID := ""
	if first.ChatType != "" && first.ChatID != "" {
		sessionID = batch.Runtime.ID + ":" + first.ChatType + ":" + first.ChatID
	}
	observationInput, attachmentPayloads := observationInputPayload(input)
	attempt, err := p.Observability.Start(observability.AttemptMetadata{TraceID: first.TraceID, Name: "codex.turn", SessionID: sessionID, UserID: first.Actor.OpenID, Input: observationInput, Metadata: metadata})
	if err != nil {
		slog.Error("observability_start", "event", "observability_start_failed", "error", err)
		return nil
	}
	for _, payload := range attachmentPayloads {
		attempt.RecordBusinessPayload("attachment.content", payload)
	}
	return attempt
}

func observationInputPayload(input []TextInput) (any, []map[string]any) {
	manifest := make([]map[string]any, 0, len(input))
	attachments := make([]map[string]any, 0)
	for _, item := range input {
		path := item.Path
		if item.ObservationPath != "" {
			path = item.ObservationPath
		}
		entry := map[string]any{"type": item.Type, "text": item.Text, "path": path, "detail": item.Detail}
		manifest = append(manifest, entry)
		if path == "" {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			attachments = append(attachments, map[string]any{"path": path, "read_error": err.Error()})
			continue
		}
		attachments = append(attachments, map[string]any{"path": path, "content_encoding": "base64", "content_base64": base64.StdEncoding.EncodeToString(body)})
	}
	return manifest, attachments
}

func (p Processor) recordCompletedUsage(ctx context.Context, batch worker.Batch, completedTurns []TurnCompleted) (observability.SessionUsageTotal, error) {
	if p.UsageLedger == nil || len(batch.Messages) == 0 {
		return observability.SessionUsageTotal{}, nil
	}
	message := batch.Messages[0]
	var latest observability.SessionUsageTotal
	for _, completed := range completedTurns {
		if completed.TurnID == "" {
			continue
		}
		session := observability.SessionUsageKey{AppID: batch.Runtime.ID, ChatType: message.ChatType, ChatID: message.ChatID}
		var total observability.SessionUsageTotal
		var err error
		if completed.Usage != nil {
			total, _, err = p.UsageLedger.RecordTurnUsage(ctx, observability.TurnUsageRecord{TraceID: message.TraceID, ThreadID: completed.ThreadID, TurnID: completed.TurnID, Session: session, Usage: *completed.Usage})
		} else if completed.SnapshotUsage != nil {
			total, err = p.UsageLedger.RecordThreadUsageSnapshot(ctx, observability.ThreadUsageSnapshotRecord{TraceID: message.TraceID, ThreadID: completed.ThreadID, TurnID: completed.TurnID, Session: session, Usage: *completed.SnapshotUsage})
		} else {
			continue
		}
		if err != nil {
			return observability.SessionUsageTotal{}, fmt.Errorf("record completed turn usage: %w", err)
		}
		latest = total
	}
	return latest, nil
}

// FormatNormalMessages preserves each ordinary user body and prefixes the
// receipt timestamp that the Router captured before FIFO delay. Legacy tests
// and non-router callers may leave ReceivedAt zero; production ingress never
// does, so their body remains usable without inventing a false timestamp.
func FormatNormalMessages(messages []worker.Message) string {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return strings.Join(messageQueries(messages), "\n")
	}
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		if message.Query == "" {
			continue
		}
		if message.ReceivedAt.IsZero() {
			parts = append(parts, message.Query)
			continue
		}
		now := message.ReceivedAt.In(location).Format(time.RFC3339)
		parts = append(parts, "<now timezone=\"Asia/Shanghai\">"+now+"</now>\n"+message.Query)
	}
	return strings.Join(parts, "\n")
}

func messageQueries(messages []worker.Message) []string {
	queries := make([]string, 0, len(messages))
	for _, message := range messages {
		if message.Query != "" {
			queries = append(queries, message.Query)
		}
	}
	return queries
}

func FeishuDynamicTools() []DynamicTool {
	return []DynamicTool{
		{Name: "message_send_current_channel", Namespace: "feishu", Description: "Send plain text to the current Feishu conversation.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["text"],"properties":{"text":{"type":"string","minLength":1,"maxLength":10000}}}`)},
		{Name: "file_upload_and_send_current_channel", Namespace: "feishu", Description: "Upload an ordinary local file and send it to the current Feishu conversation. Supported image files are sent as native Feishu images; other files are sent as attachments. Pass file_path as an absolute path.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["file_path"],"properties":{"file_path":{"type":"string","minLength":1},"display_name":{"type":"string","minLength":1,"maxLength":255}}}`)},
		{Name: "doc_create_and_announce", Namespace: "feishu", Description: "Create a Feishu document from any local UTF-8 Markdown file and announce it in the current conversation. Pass markdown_ref as an absolute path.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["markdown_ref"],"properties":{"markdown_ref":{"type":"string","minLength":1},"title":{"type":"string","minLength":1,"maxLength":800}}}`)},
		{Name: "doc_read", Namespace: "feishu", Description: "Read the plain-text content of a Feishu docx URL using the current Feishu App authorization. Pass document_url as the full https docx URL.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["document_url"],"properties":{"document_url":{"type":"string","minLength":1}}}`)},
	}
}

// S06DynamicTools is the complete catalog a newly started S06 Thread must
// receive. Keep it separate from the S05 helper so an old Thread cannot be
// mistaken for an upgraded one merely because a handler exists in-process.
func S06DynamicTools() []DynamicTool {
	tools := FeishuDynamicTools()
	return append(tools,
		DynamicTool{Name: "list_own", Namespace: "schedule", Description: "List only the current sender's scheduled tasks in this conversation.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"page_size":{"type":"integer","minimum":1,"maximum":100},"cursor":{"type":"string","minLength":1,"maxLength":2048}}}`)},
		DynamicTool{Name: "create", Namespace: "schedule", Description: "Create a CronTab scheduled prompt or a direct local script command for the current sender in this conversation.", InputSchema: json.RawMessage(`{"oneOf":[{"type":"object","additionalProperties":false,"required":["kind","prompt","cron_expression","silent"],"properties":{"kind":{"const":"prompt"},"prompt":{"type":"string","minLength":1,"maxLength":16384,"description":"未来执行的 Agent 不会看到创建任务时的对话上下文。请将当前用户请求改写为一条可独立执行的用户任务指令：使用“请……”的用户口吻，保留动作、输入位置、筛选规则、交付物和交付渠道；将“发给我/给我”明确为“发送到当前飞书会话”，避免使用“你、我、这里、刚才”等依赖当前对话的指代；不得增加用户没有要求的目标、步骤或系统说明。"},"cron_expression":{"type":"string","minLength":1,"maxLength":128},"silent":{"type":"boolean"}}},{"type":"object","additionalProperties":false,"required":["kind","command","cron_expression","silent"],"properties":{"kind":{"const":"script"},"command":{"type":"string","minLength":1,"maxLength":16384,"description":"要在任务触发时由本机 bot 进程直接执行的完整 shell 命令。请使用用户请求中的实际命令，保留脚本路径、参数和必要的工作信息；该字段不是 script_id，也不需要管理员登记。"},"cron_expression":{"type":"string","minLength":1,"maxLength":128},"silent":{"type":"boolean"}}}]}`)},
		DynamicTool{Name: "update", Namespace: "schedule", Description: "Update only a current sender's scheduled task using its current version. Disable it with enabled false; there is no delete operation.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["id","version"],"properties":{"id":{"type":"string","format":"uuid","description":"list_own 返回的任务 id；直接复用该字段，不要改名为 task_id。"},"version":{"type":"integer","minimum":1},"kind":{"enum":["prompt","script"],"description":"可选。若直接复用 list_own 返回的任务类型，可一并传入；它必须与原任务类型一致，不能用来改变任务类型。"},"cron_expression":{"type":"string","minLength":1,"maxLength":128},"silent":{"type":"boolean"},"enabled":{"type":"boolean"},"prompt":{"type":"string","minLength":1,"maxLength":16384,"description":"未来执行的 Agent 不会看到创建任务时的对话上下文。请将当前用户请求改写为一条可独立执行的用户任务指令：使用“请……”的用户口吻，保留动作、输入位置、筛选规则、交付物和交付渠道；将“发给我/给我”明确为“发送到当前飞书会话”，避免使用“你、我、这里、刚才”等依赖当前对话的指代；不得增加用户没有要求的目标、步骤或系统说明。"},"command":{"type":"string","minLength":1,"maxLength":16384,"description":"要在任务触发时由本机 bot 进程直接执行的完整 shell 命令；使用实际命令，不需要 script_id 或管理员登记。"}},"anyOf":[{"required":["cron_expression"]},{"required":["silent"]},{"required":["enabled"]},{"required":["prompt"]},{"required":["command"]}],"not":{"required":["prompt","command"]}}`)},
	)
}

func durationMilliseconds(startedAt, endedAt time.Time) int64 {
	if startedAt.IsZero() || endedAt.Before(startedAt) {
		return 0
	}
	return endedAt.Sub(startedAt).Milliseconds()
}
