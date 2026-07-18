package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

var (
	ErrDuplicate = errors.New("duplicate feishu event")
	ErrIgnored   = errors.New("ignored incoming message")
)

type App struct {
	ID, Name, WorkspaceDir, WorkspaceMode, Model, Effort string
}

// CommandKind is the receipt-safe classification of a slash input. Only the
// ASCII slash starts a command so visually similar user content remains an
// ordinary prompt.
type CommandKind string

const (
	CommandNormal  CommandKind = "normal"
	CommandNew     CommandKind = "new"
	CommandCancel  CommandKind = "cancel"
	CommandStatus  CommandKind = "status"
	CommandGoal    CommandKind = "goal"
	CommandHelp    CommandKind = "help"
	CommandInvalid CommandKind = "invalid"
)

type Command struct {
	Kind     CommandKind
	Argument string
}

// ClassifyCommand implements the deliberately small S06 grammar. It is pure
// so classification happens before attachment staging or any other effect.
func ClassifyCommand(text string) Command {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return Command{Kind: CommandNormal}
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return Command{Kind: CommandNormal}
	}
	token := strings.ToLower(fields[0])
	argument := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
	switch token {
	case "/new":
		if argument == "" {
			return Command{Kind: CommandNew}
		}
	case "/cancel", "/stop":
		if argument == "" {
			return Command{Kind: CommandCancel}
		}
	case "/status":
		if argument == "" {
			return Command{Kind: CommandStatus}
		}
	case "/help":
		if argument == "" {
			return Command{Kind: CommandHelp}
		}
	case "/goal":
		if argument != "" && utf8.RuneCountInString(argument) <= 4000 {
			return Command{Kind: CommandGoal, Argument: argument}
		}
	}
	return Command{Kind: CommandInvalid}
}

type Incoming struct {
	App          App
	EventID      string
	MessageID    string
	ChatType     string
	ChatID       string
	SenderOpenID string
	Text         string
	ReceivedAt   time.Time
	Attachments  []AttachmentReference
}

// AttachmentReference contains the short-lived Feishu resource reference from
// a validated inbound event. It must be encrypted before persistence.
type AttachmentReference struct {
	Kind         storage.AttachmentKind
	ResourceKey  string
	OriginalName string
	DeclaredMIME string
}

type MessageInput = storage.MessageInput
type MessageRecord = storage.MessageRecord

type Store interface {
	PersistIncoming(context.Context, string, string, string, storage.MessageInput) (storage.MessageRecord, bool, error)
	FailMessage(context.Context, string, string, string, int64) error
	CompleteMessage(context.Context, string, string, string, int64) error
	RecordCommandReplyFailure(context.Context, string, string, string, string) error
}

type Handler struct {
	store        Store
	dispatcher   workerDispatcher
	runtime      App
	rejector     rejectionSender
	availability availability
	resetter     sessionResetter
	account      accountReader
	commands     commandResponder
	protector    attachmentProtector
}

type attachmentProtector interface {
	Seal(appID, attachmentID, sourceMessageID, resourceKey string) ([]byte, int, error)
}

type workerDispatcher interface {
	Accept(context.Context, worker.Message) error
}

type workerCanceller interface {
	Cancel(context.Context, worker.Key) error
}

type workerController interface {
	SubmitControl(context.Context, worker.Key, worker.Control) error
}

type sessionResetter interface {
	NewSession(context.Context, string) error
}

type accountReader interface {
	Call(context.Context, string, any) (json.RawMessage, error)
}

func New(store Store, dispatcher workerDispatcher) *Handler {
	return &Handler{store: store, dispatcher: dispatcher}
}
func (h *Handler) SetAppRuntime(app App) { h.runtime = app }

type rejectionSender interface {
	SendText(context.Context, string, string, string) (string, error)
}

type commandResponder interface {
	SendStaticCard(context.Context, string, string, string) (string, error)
	SendCommandText(context.Context, string, string, string) (string, error)
}

func (h *Handler) SetRejectionSender(sender rejectionSender)      { h.rejector = sender }
func (h *Handler) SetCommandResponder(responder commandResponder) { h.commands = responder }

type availability interface{ IsReady() bool }

func (h *Handler) SetAvailability(runtime availability)        { h.availability = runtime }
func (h *Handler) SetSessionResetter(resetter sessionResetter) { h.resetter = resetter }
func (h *Handler) SetAccountReader(reader accountReader)       { h.account = reader }
func (h *Handler) SetAttachmentProtector(protector attachmentProtector) {
	h.protector = protector
}

func (h *Handler) Handle(ctx context.Context, incoming Incoming) error {
	if incoming.App.WorkspaceDir == "" && h.runtime.ID == incoming.App.ID {
		incoming.App = h.runtime
	}
	if err := validate(incoming); err != nil {
		return err
	}
	command := ClassifyCommand(incoming.Text)
	if incoming.ReceivedAt.IsZero() {
		incoming.ReceivedAt = time.Now().UTC()
	}
	traceID := storage.TraceID(incoming.App.ID, incoming.EventID)
	attachments, attachmentIDs, err := h.stageAttachments(incoming)
	if err != nil {
		return fmt.Errorf("stage attachment references: %w", err)
	}
	receipt := commandReceipt(command, incoming.Text, len(attachments) > 0)
	message, duplicate, err := h.store.PersistIncoming(ctx, incoming.App.ID, incoming.ChatType, incoming.ChatID, MessageInput{
		ID:                   uuid.NewString(),
		TraceID:              traceID,
		FeishuEventID:        incoming.EventID,
		FeishuUserMessageID:  incoming.MessageID,
		SenderOpenID:         incoming.SenderOpenID,
		UserContent:          receipt.content,
		ReceivedAt:           incoming.ReceivedAt.UTC(),
		CommandKind:          receipt.kind,
		CommandPayloadSHA256: receipt.digest,
		CommandPayloadBytes:  receipt.bytes,
		Attachments:          attachments,
	})
	if err != nil {
		return fmt.Errorf("persist incoming message: %w", err)
	}
	if duplicate {
		slog.Info("message_deduplicated", "app_id", incoming.App.ID, "channel_key", incoming.ChatType+":"+incoming.ChatID, "trace_id", traceID, "event", "message_deduplicated")
		return ErrDuplicate
	}
	key, target := routing(incoming)
	if command.Kind == CommandCancel || command.Kind == CommandNew {
		if controller, ok := h.dispatcher.(workerController); ok {
			if err := controller.SubmitControl(ctx, key, worker.Control{Run: func(controlCtx context.Context) error {
				reply := "已请求停止当前任务。"
				if command.Kind == CommandNew {
					if h.resetter == nil {
						return h.failAndReply(controlCtx, message.ID, target, "command_archive_failed", "未能安全开启新对话，当前会话保持不变。")
					}
					if err := h.resetter.NewSession(controlCtx, message.ChatGroupID); err != nil {
						return h.failAndReply(controlCtx, message.ID, target, "command_archive_failed", "未能安全开启新对话，当前会话保持不变。")
					}
					reply = "已开启新对话。下一条普通消息将创建新的 Codex 会话。"
				}
				return h.completeAndReply(controlCtx, message.ID, target, reply)
			}, OnError: func(controlCtx context.Context, err error) {
				slog.Error("command_control_failed", "event", "command_control_failed", "message_id", message.ID, "error", err)
				_ = h.store.FailMessage(controlCtx, message.ID, "command_control_failed", "命令处理未完成，请稍后使用 /status。", 0)
			}}); err != nil {
				return fmt.Errorf("submit channel control: %w", err)
			}
			slog.Info("worker_control_accepted", "app_id", incoming.App.ID, "channel_key", key.String(), "trace_id", traceID, "event", "worker_control_accepted")
			return nil
		}
		if canceller, ok := h.dispatcher.(workerCanceller); ok {
			if err := canceller.Cancel(ctx, key); err != nil {
				return fmt.Errorf("cancel active worker: %w", err)
			}
		}
		reply := "已请求停止当前任务。"
		if command.Kind == CommandNew {
			if h.resetter == nil {
				return fmt.Errorf("new session resetter is unavailable")
			}
			if err := h.resetter.NewSession(ctx, message.ChatGroupID); err != nil {
				return fmt.Errorf("start new session: %w", err)
			}
			reply = "已开始新会话。"
		}
		botMessageID := ""
		if h.rejector != nil {
			botMessageID, _ = h.rejector.SendText(ctx, target.ID, target.Type, reply)
		}
		if err := h.store.CompleteMessage(ctx, message.ID, botMessageID, reply, 0); err != nil {
			return fmt.Errorf("complete stop command: %w", err)
		}
		slog.Info("worker_stop_requested", "app_id", incoming.App.ID, "channel_key", key.String(), "trace_id", traceID, "event", "worker_stop_requested")
		return nil
	}
	if command.Kind == CommandGoal {
		job := worker.Message{ID: message.ID, TraceID: traceID, ChatGroupID: message.ChatGroupID, ChatType: incoming.ChatType, ChatID: incoming.ChatID, Key: key, Runtime: worker.AppRuntime{ID: incoming.App.ID, WorkspaceDir: incoming.App.WorkspaceDir, WorkspaceMode: incoming.App.WorkspaceMode, Model: incoming.App.Model, Effort: incoming.App.Effort}, Reply: target, Actor: worker.ActorPrincipal{OpenID: incoming.SenderOpenID}, Query: command.Argument, ReceivedAt: incoming.ReceivedAt.UTC(), Goal: true}
		if err := h.dispatcher.Accept(ctx, job); err != nil {
			return h.failAndReply(ctx, message.ID, target, "goal_worker_rejected", "当前队列暂不可用，请稍后重新发送。")
		}
		slog.Info("goal_worker_accepted", "app_id", incoming.App.ID, "channel_key", key.String(), "trace_id", traceID, "event", "goal_worker_accepted")
		return nil
	}
	if command.Kind == CommandStatus {
		reply := "账户状态暂不可用，请稍后重试。"
		code := "status_unavailable"
		if h.account != nil {
			if rendered, err := renderStatus(ctx, h.account); err == nil {
				reply, code = rendered, ""
			}
		}
		if code != "" {
			return h.failAndReply(ctx, message.ID, target, code, reply)
		}
		return h.completeAndReply(ctx, message.ID, target, reply)
	}
	if command.Kind == CommandHelp {
		return h.completeAndReply(ctx, message.ID, target, "可用命令：/new、/cancel、/status、/goal <目标>、/help")
	}
	if command.Kind == CommandInvalid {
		return h.failAndReply(ctx, message.ID, target, "command_invalid", "命令无效。请发送 /help 查看可用命令。")
	}
	if h.availability != nil && !h.availability.IsReady() {
		if updateErr := h.store.FailMessage(ctx, message.ID, "app_server_unavailable", "Codex App Server 暂不可用，请稍后重试。", 0); updateErr != nil {
			return fmt.Errorf("mark app server unavailable: %w", updateErr)
		}
		if h.rejector != nil {
			_, _ = h.rejector.SendText(ctx, target.ID, target.Type, "Codex App Server 暂不可用，请稍后重试。")
		}
		return fmt.Errorf("app server unavailable for %s", key.String())
	}
	job := worker.Message{ID: message.ID, TraceID: traceID, ChatGroupID: message.ChatGroupID, ChatType: incoming.ChatType, ChatID: incoming.ChatID, Key: key, Runtime: worker.AppRuntime{ID: incoming.App.ID, WorkspaceDir: incoming.App.WorkspaceDir, WorkspaceMode: incoming.App.WorkspaceMode, Model: incoming.App.Model, Effort: incoming.App.Effort}, Reply: target, Actor: worker.ActorPrincipal{OpenID: incoming.SenderOpenID}, Query: incoming.Text, ReceivedAt: incoming.ReceivedAt.UTC(), HasRequiredAttachment: len(attachmentIDs) > 0, AttachmentIDs: attachmentIDs}
	if err := h.dispatcher.Accept(ctx, job); err != nil {
		if updateErr := h.store.FailMessage(ctx, message.ID, "worker_rejected", safeError(err), 0); updateErr != nil {
			return fmt.Errorf("queue message: %w; mark failed: %v", err, updateErr)
		}
		if h.rejector != nil {
			_, _ = h.rejector.SendText(ctx, target.ID, target.Type, "当前队列暂不可用，请稍后重新发送。")
		}
		return fmt.Errorf("queue message: %w", err)
	}
	slog.Info("worker_accepted", "app_id", incoming.App.ID, "channel_key", key.String(), "trace_id", traceID, "event", "worker_accepted")
	return nil
}

func renderStatus(parent context.Context, reader accountReader) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	type result struct {
		raw json.RawMessage
		err error
	}
	var rates, usage result
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		rates.raw, rates.err = reader.Call(ctx, "account/rateLimits/read", map[string]any{})
	}()
	go func() {
		defer group.Done()
		usage.raw, usage.err = reader.Call(ctx, "account/usage/read", map[string]any{})
	}()
	group.Wait()
	if rates.err != nil && usage.err != nil {
		return "", errors.New("account status unavailable")
	}
	lines := []string{"📊 Codex 状态", "查询时间：" + time.Now().In(shanghaiLocation()).Format("2006-01-02 15:04:05 MST")}
	if rates.err != nil {
		lines = append(lines, "额度：暂不可用")
	} else {
		lines = append(lines, statusRateLines(rates.raw)...)
	}
	if usage.err != nil {
		lines = append(lines, "用量摘要：暂不可用")
	} else {
		lines = append(lines, "用量摘要：已获取")
	}
	return strings.Join(lines, "\n"), nil
}

func shanghaiLocation() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*60*60)
	}
	return location
}

func statusRateLines(raw json.RawMessage) []string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return []string{"额度：暂不可用"}
	}
	buckets := make([]map[string]any, 0)
	findRateBuckets(value, &buckets)
	if len(buckets) == 0 {
		return []string{"额度：未提供"}
	}
	lines := make([]string, 0, len(buckets))
	for _, bucket := range buckets {
		used, ok := number(bucket["usedPercent"])
		if !ok {
			continue
		}
		line := fmt.Sprintf("额度：已用 %.0f%%", used)
		if minutes, ok := number(bucket["windowDurationMins"]); ok && minutes >= 0 {
			line += fmt.Sprintf("（窗口 %.0f 分钟", minutes)
			if reset, ok := number(bucket["resetsAt"]); ok && reset > 0 {
				line += "，重置 " + time.Unix(int64(reset), 0).In(shanghaiLocation()).Format("01-02 15:04 MST")
			}
			line += "）"
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return []string{"额度：未提供"}
	}
	return lines
}

func findRateBuckets(value any, buckets *[]map[string]any) {
	switch typed := value.(type) {
	case map[string]any:
		if _, ok := typed["usedPercent"]; ok {
			*buckets = append(*buckets, typed)
		}
		for _, child := range typed {
			findRateBuckets(child, buckets)
		}
	case []any:
		for _, child := range typed {
			findRateBuckets(child, buckets)
		}
	}
}

func number(value any) (float64, bool) {
	valueNumber, ok := value.(float64)
	return valueNumber, ok
}

func (h *Handler) completeAndReply(ctx context.Context, messageID string, target worker.ReplyTarget, reply string) error {
	botMessageID := ""
	if h.commands != nil {
		var err error
		botMessageID, err = h.commands.SendStaticCard(ctx, target.ID, target.Type, reply)
		if err != nil && errors.Is(err, worker.ErrCommandDeliveryRejected) {
			botMessageID, err = h.commands.SendCommandText(ctx, target.ID, target.Type, reply)
			if err != nil {
				if errors.Is(err, worker.ErrCommandDeliveryRejected) {
					return h.store.RecordCommandReplyFailure(ctx, messageID, "succeeded", "rejected", "命令已处理，但未能发送确认消息。")
				}
				return h.store.RecordCommandReplyFailure(ctx, messageID, "succeeded", "unknown", "命令已处理，但确认消息状态未知。")
			}
		} else if err != nil {
			return h.store.RecordCommandReplyFailure(ctx, messageID, "succeeded", "unknown", "命令已处理，但确认消息状态未知。")
		}
	} else if h.rejector != nil {
		botMessageID, _ = h.rejector.SendText(ctx, target.ID, target.Type, reply)
	}
	return h.store.CompleteMessage(ctx, messageID, botMessageID, reply, 0)
}

func (h *Handler) failAndReply(ctx context.Context, messageID string, target worker.ReplyTarget, code, reply string) error {
	if h.commands != nil {
		if _, err := h.commands.SendStaticCard(ctx, target.ID, target.Type, reply); err == nil {
			return h.store.FailMessage(ctx, messageID, code, reply, 0)
		} else if errors.Is(err, worker.ErrCommandDeliveryUnknown) {
			return h.store.RecordCommandReplyFailure(ctx, messageID, "failed", "unknown", "命令处理失败，确认消息状态未知。")
		} else if _, fallbackErr := h.commands.SendCommandText(ctx, target.ID, target.Type, reply); fallbackErr == nil {
			return h.store.FailMessage(ctx, messageID, code, reply, 0)
		} else if errors.Is(fallbackErr, worker.ErrCommandDeliveryUnknown) {
			return h.store.RecordCommandReplyFailure(ctx, messageID, "failed", "unknown", "命令处理失败，确认消息状态未知。")
		} else {
			return h.store.RecordCommandReplyFailure(ctx, messageID, "failed", "rejected", "命令处理失败，未能发送确认消息。")
		}
	}
	if h.rejector != nil {
		_, _ = h.rejector.SendText(ctx, target.ID, target.Type, reply)
	}
	return h.store.FailMessage(ctx, messageID, code, reply, 0)
}

type commandReceiptFields struct {
	content, kind, digest string
	bytes                 int
}

func commandReceipt(command Command, text string, hasAttachments bool) commandReceiptFields {
	if command.Kind == CommandNormal {
		return commandReceiptFields{content: safeIncomingContent(text, hasAttachments)}
	}
	if command.Kind == CommandGoal {
		digest := sha256.Sum256([]byte(command.Argument))
		return commandReceiptFields{content: "/goal [redacted]", kind: string(command.Kind), digest: hex.EncodeToString(digest[:]), bytes: len([]byte(command.Argument))}
	}
	if command.Kind == CommandInvalid {
		fields := strings.Fields(strings.TrimSpace(text))
		if len(fields) == 0 {
			return commandReceiptFields{content: "/ [invalid]", kind: string(command.Kind)}
		}
		return commandReceiptFields{content: fields[0] + " [invalid]", kind: string(command.Kind)}
	}
	return commandReceiptFields{content: "/" + string(command.Kind), kind: string(command.Kind)}
}

func (h *Handler) stageAttachments(incoming Incoming) ([]storage.AttachmentInput, []string, error) {
	if len(incoming.Attachments) == 0 {
		return nil, nil, nil
	}
	if h.protector == nil {
		return nil, nil, fmt.Errorf("attachment protector is unavailable")
	}
	attachments := make([]storage.AttachmentInput, 0, len(incoming.Attachments))
	ids := make([]string, 0, len(incoming.Attachments))
	for _, reference := range incoming.Attachments {
		if reference.ResourceKey == "" || reference.Kind != storage.AttachmentImage && reference.Kind != storage.AttachmentFile {
			return nil, nil, fmt.Errorf("attachment reference is invalid")
		}
		id := uuid.NewString()
		ciphertext, version, err := h.protector.Seal(incoming.App.ID, id, incoming.MessageID, reference.ResourceKey)
		if err != nil {
			return nil, nil, err
		}
		attachments = append(attachments, storage.AttachmentInput{ID: id, Kind: reference.Kind, SourceResourceRefEnc: ciphertext, SourceRefKeyVersion: version, SourceMessageID: incoming.MessageID, OriginalNameSafe: reference.OriginalName, DeclaredMIME: reference.DeclaredMIME})
		ids = append(ids, id)
	}
	return attachments, ids, nil
}

func safeIncomingContent(text string, hasAttachments bool) string {
	if text != "" {
		return text
	}
	if hasAttachments {
		return "[attachment]"
	}
	return ""
}

func validate(incoming Incoming) error {
	if incoming.ChatType == "topic_group" || incoming.ChatType != "p2p" && incoming.ChatType != "group" {
		return ErrIgnored
	}
	if incoming.App.ID == "" || incoming.App.Name == "" || incoming.EventID == "" || incoming.MessageID == "" || incoming.ChatID == "" || incoming.Text == "" && len(incoming.Attachments) == 0 {
		return ErrIgnored
	}
	if incoming.ChatType == "p2p" && incoming.SenderOpenID == "" {
		return ErrIgnored
	}
	return nil
}

func routing(incoming Incoming) (worker.Key, worker.ReplyTarget) {
	if incoming.ChatType == "p2p" {
		return worker.P2PKey(incoming.SenderOpenID, incoming.App.ID), worker.ReplyTarget{ID: incoming.SenderOpenID, Type: "open_id"}
	}
	return worker.GroupKey(incoming.ChatID, incoming.App.ID), worker.ReplyTarget{ID: incoming.ChatID, Type: "chat_id"}
}

func safeError(err error) string {
	return "failed to send fixed reply"
}
