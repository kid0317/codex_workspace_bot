package feishuaction

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/kid0317/codex-workspace-bot/internal/codexapp"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

type Client interface {
	SendCurrentText(context.Context, worker.ReplyTarget, string) (string, error)
	UploadAndSend(context.Context, worker.ReplyTarget, *os.File, string) (fileKey, messageID string, err error)
	CreateDocumentAndAnnounce(context.Context, worker.ReplyTarget, string, string, []byte) (worker.DocumentOutcome, error)
	ReadDocument(context.Context, string) (string, error)
}

type Route struct {
	AppID, ChannelKey, ChatGroupID string
	Reply                          worker.ReplyTarget
	// OwnerOpenID is copied from the ingress actor. It is never supplied by
	// dynamic-tool arguments and must not be logged or persisted by this layer.
	OwnerOpenID  string
	WorkspaceDir string
	OutboxDir    string
}

type Ledger interface {
	ClaimAction(context.Context, storage.ActionCall) (storage.ActionClaim, error)
	StartAction(context.Context, string, string, string, string) (bool, error)
}
type ResultLedger interface {
	CompleteAction(context.Context, storage.ActionResult) error
}
type ReplayLedger interface {
	GetCompletedAction(context.Context, string, string, string, string) (storage.CompletedAction, bool, error)
}

type Service struct {
	Clients          map[string]Client
	MaxFileBytes     int64
	MaxMarkdownBytes int64
	Ledger           Ledger
	ResultLedger     ResultLedger
	ReplayLedger     ReplayLedger
	Protector        *ResultProtector
}

func (s Service) Execute(ctx context.Context, route Route, call codexapp.ToolCall) (codexapp.ToolResult, error) {
	call.Tool = canonicalToolName(call.Tool)
	result, terminalState, finalize, err := s.execute(ctx, route, call)
	if err != nil || !finalize || s.ResultLedger == nil || s.Protector == nil || len(result.ContentItems) == 0 {
		return result, err
	}
	ciphertext, version, sealErr := s.Protector.Seal(route.AppID, call.ThreadID, call.TurnID, call.CallID, call.Tool, []byte(result.ContentItems[0].Text))
	if sealErr != nil {
		return toolFailure("action result could not be secured"), nil
	}
	if completeErr := s.ResultLedger.CompleteAction(ctx, storage.ActionResult{AppID: route.AppID, ThreadID: call.ThreadID, TurnID: call.TurnID, CallID: call.CallID, ResultEnc: ciphertext, ResultKeyVersion: version, State: terminalState}); completeErr != nil {
		return toolFailure("action result could not be recorded"), nil
	}
	return result, nil
}

func canonicalToolName(tool string) string {
	switch tool {
	case "message_send_current_channel":
		return codexapp.FeishuToolMessageSendCurrentChannel
	case "file_upload_and_send_current_channel":
		return codexapp.FeishuToolFileUploadAndSendCurrentChannel
	case "doc_create_and_announce":
		return codexapp.FeishuToolDocCreateAndAnnounce
	case "doc_read":
		return codexapp.FeishuToolDocRead
	default:
		return tool
	}
}

func (s Service) execute(ctx context.Context, route Route, call codexapp.ToolCall) (codexapp.ToolResult, storage.ActionState, bool, error) {
	digest := sha256.Sum256(call.Arguments)
	digestText := fmt.Sprintf("%x", digest)
	if s.ReplayLedger != nil && s.Protector != nil {
		replay, found, err := s.ReplayLedger.GetCompletedAction(ctx, route.AppID, call.ThreadID, call.TurnID, call.CallID)
		if err != nil {
			return toolFailure("action replay could not be read"), "", false, nil
		}
		if found {
			if replay.Tool != call.Tool || replay.ArgumentsDigest != digestText {
				return toolFailure("action call conflicts with prior arguments"), "", false, nil
			}
			plaintext, openErr := s.Protector.Open(route.AppID, call.ThreadID, call.TurnID, call.CallID, call.Tool, replay.ResultEnc, replay.ResultKeyVersion)
			if openErr != nil {
				return toolFailure("action replay could not be decrypted"), "", false, nil
			}
			if replay.State == storage.ActionSucceeded {
				return toolSuccess(string(plaintext)), "", false, nil
			}
			return toolFailure(string(plaintext)), "", false, nil
		}
	}
	client := s.Clients[route.AppID]
	if client == nil || route.Reply.ID == "" || route.Reply.Type == "" {
		return toolFailure("current channel is unavailable"), "", false, nil
	}
	claimed := false
	if s.Ledger != nil {
		claim, err := s.Ledger.ClaimAction(ctx, storage.ActionCall{ID: uuid.NewString(), AppID: route.AppID, ChannelKey: route.ChannelKey, ChatGroupID: route.ChatGroupID, ThreadID: call.ThreadID, TurnID: call.TurnID, CallID: call.CallID, Tool: call.Tool, ArgumentsDigest: digestText})
		if err != nil {
			return toolFailure("action could not be recorded"), "", false, nil
		}
		if claim != storage.ActionClaimed {
			return toolFailure("action call is in progress or was already handled"), "", false, nil
		}
		claimed = true
	}
	if ctx.Err() != nil {
		return actionResult(toolFailure("action was cancelled before it was sent"), storage.ActionCancelledBeforeSend, claimed)
	}
	startExternal := func() (codexapp.ToolResult, bool) {
		if !claimed {
			return codexapp.ToolResult{}, true
		}
		started, err := s.Ledger.StartAction(ctx, route.AppID, call.ThreadID, call.TurnID, call.CallID)
		if err != nil || !started {
			return toolFailure("action could not be started"), false
		}
		return codexapp.ToolResult{}, true
	}
	switch call.Tool {
	case codexapp.FeishuToolMessageSendCurrentChannel:
		var args struct {
			Text string `json:"text"`
		}
		if !decodeArguments(call.Arguments, &args) || args.Text == "" || len(args.Text) > 10_000 {
			return actionResult(toolFailure("message parameters are invalid"), storage.ActionRejected, claimed)
		}
		if result, ok := startExternal(); !ok {
			return actionResult(result, storage.ActionUnknown, claimed)
		}
		if _, err := client.SendCurrentText(ctx, route.Reply, args.Text); err != nil {
			return actionResult(toolFailure("message could not be sent"), actionExternalFailureState(err), claimed)
		}
		return actionResult(toolSuccess(`{"outcome":"sent"}`), storage.ActionSucceeded, claimed)
	case codexapp.FeishuToolFileUploadAndSendCurrentChannel:
		var args struct {
			FilePath    string `json:"file_path"`
			DisplayName string `json:"display_name"`
		}
		if !decodeArguments(call.Arguments, &args) {
			return actionResult(toolFailure("file parameters are invalid"), storage.ActionRejected, claimed)
		}
		max := s.MaxFileBytes
		if max <= 0 {
			max = 30_000_000
		}
		file, name, _, err := OpenOrdinaryFile(resolveLocalReference(route.WorkspaceDir, args.FilePath), args.DisplayName, max)
		if err != nil {
			return actionResult(toolFailure("file is not an allowed ordinary file"), storage.ActionRejected, claimed)
		}
		defer file.Close()
		if result, ok := startExternal(); !ok {
			return actionResult(result, storage.ActionUnknown, claimed)
		}
		if _, _, err := client.UploadAndSend(ctx, route.Reply, file, name); err != nil {
			return actionResult(toolFailure("file could not be sent"), actionExternalFailureState(err), claimed)
		}
		return actionResult(toolSuccess(`{"outcome":"sent"}`), storage.ActionSucceeded, claimed)
	case codexapp.FeishuToolDocCreateAndAnnounce:
		var args struct {
			MarkdownRef string `json:"markdown_ref"`
			Title       string `json:"title"`
		}
		if !decodeArguments(call.Arguments, &args) || args.MarkdownRef == "" || args.Title == "" || len(args.Title) > 800 {
			return actionResult(toolFailure("document parameters are invalid"), storage.ActionRejected, claimed)
		}
		if route.OwnerOpenID == "" {
			return actionResult(toolFailure("document owner is unavailable for this message"), storage.ActionRejected, claimed)
		}
		max := s.MaxMarkdownBytes
		if max <= 0 {
			max = 2_000_000
		}
		markdown, err := ReadMarkdownFile(resolveLocalReference(route.WorkspaceDir, args.MarkdownRef), max)
		if err != nil {
			return actionResult(toolFailure("markdown is not an available local UTF-8 file"), storage.ActionRejected, claimed)
		}
		if result, ok := startExternal(); !ok {
			return actionResult(result, storage.ActionUnknown, claimed)
		}
		outcome, err := client.CreateDocumentAndAnnounce(ctx, route.Reply, route.OwnerOpenID, args.Title, markdown)
		if err != nil {
			return actionResult(toolFailure("document could not be created"), actionExternalFailureState(err), claimed)
		}
		if outcome.URL == "" || outcome.AnnouncementOutcome == "" || outcome.OwnerTransferOutcome == "" {
			return actionResult(toolFailure("document outcome was invalid"), storage.ActionUnknown, claimed)
		}
		result, marshalErr := json.Marshal(map[string]any{"url": outcome.URL, "content_written": outcome.ContentWritten, "announcement_outcome": outcome.AnnouncementOutcome, "owner_transferred": outcome.OwnerTransferred, "owner_transfer_outcome": outcome.OwnerTransferOutcome})
		if marshalErr != nil {
			return actionResult(toolFailure("document result could not be prepared"), storage.ActionUnknown, claimed)
		}
		return actionResult(toolSuccess(string(result)), storage.ActionSucceeded, claimed)
	case codexapp.FeishuToolDocRead:
		var args struct {
			DocumentURL string `json:"document_url"`
		}
		if !decodeArguments(call.Arguments, &args) {
			return actionResult(toolFailure("document parameters are invalid"), storage.ActionRejected, claimed)
		}
		documentID, ok := documentIDFromReference(args.DocumentURL)
		if !ok {
			return actionResult(toolFailure("document URL is not a valid Feishu docx reference"), storage.ActionRejected, claimed)
		}
		if result, ok := startExternal(); !ok {
			return actionResult(result, storage.ActionUnknown, claimed)
		}
		content, err := client.ReadDocument(ctx, documentID)
		if err != nil {
			return actionResult(toolFailure("document could not be read with the current Feishu App authorization"), actionExternalFailureState(err), claimed)
		}
		return actionResult(toolSuccess(content), storage.ActionSucceeded, claimed)
	default:
		return actionResult(toolFailure("tool is unavailable"), storage.ActionRejected, claimed)
	}
}

func documentIDFromReference(reference string) (string, bool) {
	reference = strings.TrimSpace(reference)
	if isDocumentID(reference) {
		return reference, true
	}
	parsed, err := url.Parse(reference)
	if err != nil || parsed.Scheme != "https" || !isFeishuDocumentHost(parsed.Hostname()) {
		return "", false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 2 || parts[0] != "docx" {
		return "", false
	}
	documentID, err := url.PathUnescape(parts[1])
	if err != nil || !isDocumentID(documentID) {
		return "", false
	}
	return documentID, true
}

func isFeishuDocumentHost(host string) bool {
	return host == "feishu.cn" || strings.HasSuffix(host, ".feishu.cn") || host == "larksuite.com" || strings.HasSuffix(host, ".larksuite.com")
}

func isDocumentID(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'A' || character > 'Z' {
				if character < 'a' || character > 'z' {
					return false
				}
			}
		}
	}
	return true
}

func actionResult(result codexapp.ToolResult, state storage.ActionState, finalize bool) (codexapp.ToolResult, storage.ActionState, bool, error) {
	return result, state, finalize, nil
}

func actionExternalFailureState(err error) storage.ActionState {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "code=4") || strings.Contains(message, "code=429") {
		return storage.ActionRejected
	}
	return storage.ActionUnknown
}

func decodeArguments(raw json.RawMessage, destination any) bool {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination) == nil && decoder.Decode(&struct{}{}) != nil
}

func toolSuccess(text string) codexapp.ToolResult {
	return codexapp.ToolResult{Success: true, ContentItems: []codexapp.ToolContentItem{{Type: "inputText", Text: text}}}
}

func toolFailure(text string) codexapp.ToolResult {
	return codexapp.ToolResult{Success: false, ContentItems: []codexapp.ToolContentItem{{Type: "inputText", Text: text}}}
}

var (
	ErrInvalidFile     = errors.New("file must be a non-empty ordinary file")
	ErrFileTooLarge    = errors.New("file exceeds Feishu size limit")
	ErrInvalidMarkdown = errors.New("markdown must be a non-empty UTF-8 regular file")
)

// OpenOrdinaryFile intentionally has no workspace/session root check. This is
// a single-user local bot: the only routing authority remains the bound
// current channel, while any readable ordinary local file may be sent.
func OpenOrdinaryFile(path, displayName string, maxBytes int64) (*os.File, string, int64, error) {
	if path == "" || maxBytes <= 0 {
		return nil, "", 0, ErrInvalidFile
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, "", 0, fmt.Errorf("resolve file path: %w", err)
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, "", 0, fmt.Errorf("open file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, "", 0, fmt.Errorf("stat opened file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		_ = file.Close()
		return nil, "", 0, ErrInvalidFile
	}
	if info.Size() > maxBytes {
		_ = file.Close()
		return nil, "", 0, ErrFileTooLarge
	}
	name := safeDisplayName(displayName)
	if name == "" {
		name = safeDisplayName(filepath.Base(resolved))
	}
	if name == "" {
		_ = file.Close()
		return nil, "", 0, ErrInvalidFile
	}
	return file, name, info.Size(), nil
}

func ReadMarkdownFile(path string, maxBytes int64) ([]byte, error) {
	if path == "" || maxBytes <= 0 {
		return nil, ErrInvalidMarkdown
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrInvalidMarkdown
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxBytes {
		return nil, ErrInvalidMarkdown
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(contents)) > maxBytes || !utf8.Valid(contents) {
		return nil, ErrInvalidMarkdown
	}
	return contents, nil
}

func resolveLocalReference(workspaceDir, reference string) string {
	if filepath.IsAbs(reference) || workspaceDir == "" {
		return reference
	}
	return filepath.Join(workspaceDir, reference)
}

func safeDisplayName(name string) string {
	name = filepath.Base(strings.Map(func(r rune) rune {
		if r < 32 || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, name))
	if name == "." || name == "" || len(name) > 255 {
		return ""
	}
	return name
}
