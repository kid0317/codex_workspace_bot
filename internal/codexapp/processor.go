package codexapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

type ThreadStore interface {
	GetChatGroupThread(context.Context, string) (string, error)
	SetThreadIfExpected(context.Context, string, string, string) (bool, error)
	GetChatGroupToolset(context.Context, string) (string, error)
	SetChatGroupToolset(context.Context, string, string) error
}
type TextInput struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
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
	feishuToolsetVersion                      = "s05-feishu-v2"
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
	OnItem              func(CompletedItem) bool `json:"-"`
	ToolHandler         ToolHandler              `json:"-"`
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
	Runtime      *Runtime
	Store        ThreadStore
	Attachments  AttachmentResolver
	ToolHandlers func(worker.Batch) ToolHandler
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
	threadID, err := p.Store.GetChatGroupThread(ctx, groupID)
	if err != nil {
		return worker.ProcessResult{}, err
	}
	if p.ToolHandlers != nil {
		toolset, toolsetErr := p.Store.GetChatGroupToolset(ctx, groupID)
		if toolsetErr != nil {
			return worker.ProcessResult{}, toolsetErr
		}
		if toolset != feishuToolsetVersion {
			if threadID != "" {
				if _, archiveErr := p.Runtime.Call(ctx, "thread/archive", map[string]string{"threadId": threadID}); archiveErr != nil {
					return worker.ProcessResult{}, fmt.Errorf("archive pre-s05 thread: %w", archiveErr)
				}
				cleared, clearErr := p.Store.SetThreadIfExpected(ctx, groupID, threadID, "")
				if clearErr != nil || !cleared {
					return worker.ProcessResult{}, fmt.Errorf("clear pre-s05 thread: %w", clearErr)
				}
			}
			if setErr := p.Store.SetChatGroupToolset(ctx, groupID, feishuToolsetVersion); setErr != nil {
				return worker.ProcessResult{}, setErr
			}
			threadID = ""
		}
	}
	if threadID != "" {
		if _, err := p.Runtime.Call(ctx, "thread/resume", ThreadResumeParams{ThreadID: threadID, CWD: batch.Runtime.WorkspaceDir, Model: batch.Runtime.Model, ApprovalPolicy: "never", Sandbox: "danger-full-access"}); err == nil {
			return p.startTurn(ctx, batch, threadID)
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
		_, _ = p.Store.GetChatGroupThread(ctx, groupID)
		return worker.ProcessResult{}, fmt.Errorf("chat group thread changed concurrently")
	}
	return p.startTurn(ctx, batch, response.Thread.ID)
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
	var toolHandler ToolHandler
	if p.ToolHandlers != nil {
		toolHandler = p.ToolHandlers(batch)
	}
	startedAt, err := p.Runtime.StartTurn(ctx, threadID, TurnStartParams{CWD: batch.Runtime.WorkspaceDir, Model: batch.Runtime.Model, Effort: batch.Runtime.Effort, ApprovalPolicy: "never", ClientUserMessageID: attemptID, Input: input, Route: RouteMetadata{AppID: batch.Runtime.ID, ChannelKey: batch.Key.String(), ChatGroupID: batch.Messages[0].ChatGroupID, AttemptID: attemptID}, ToolHandler: toolHandler, OnTurnStarted: func(id string) { turnID = id }, OnItem: func(item CompletedItem) bool {
		if batch.OnItem == nil {
			return true
		}
		return batch.OnItem(worker.PresentationItem{ID: item.ID, Type: item.Type, Phase: item.Phase, Text: item.Text})
	}})
	return worker.ProcessResult{DurationMS: durationMilliseconds(startedAt, p.Runtime.cfg.Now()), ThreadID: threadID, TurnID: turnID}, err
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

func durationMilliseconds(startedAt, endedAt time.Time) int64 {
	if startedAt.IsZero() || endedAt.Before(startedAt) {
		return 0
	}
	return endedAt.Sub(startedAt).Milliseconds()
}
