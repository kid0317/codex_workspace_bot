package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kid0317/codex-workspace-bot/internal/db"
	"github.com/kid0317/codex-workspace-bot/internal/engine"
	"github.com/kid0317/codex-workspace-bot/internal/feishu"
	"github.com/kid0317/codex-workspace-bot/internal/guardrail"
	"github.com/kid0317/codex-workspace-bot/internal/model"
	"github.com/kid0317/codex-workspace-bot/internal/observability"
	"github.com/kid0317/codex-workspace-bot/internal/output"
	"github.com/kid0317/codex-workspace-bot/internal/sessionctx"
)

type Options struct {
	WorkspaceMode     string
	WorkspaceDir      string
	Guardrail         guardrail.Guardrail
	QueueSize         int
	WorkerIdleTimeout time.Duration
	ApprovalTimeout   time.Duration
	Emitter           observability.Emitter
}

type Manager struct {
	store   *db.Store
	engine  engine.Engine
	sender  feishu.Sender
	opts    Options
	mu      sync.Mutex
	workers map[string]*channelWorker
	closed  bool
	wg      sync.WaitGroup
}

func NewManager(store *db.Store, eng engine.Engine, sender feishu.Sender, opts Options) *Manager {
	if opts.WorkspaceMode == "" {
		opts.WorkspaceMode = "work"
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = 64
	}
	if opts.WorkerIdleTimeout <= 0 {
		opts.WorkerIdleTimeout = 30 * time.Minute
	}
	if opts.Emitter == nil {
		opts.Emitter = observability.NopEmitter{}
	}
	return &Manager{store: store, engine: eng, sender: sender, opts: opts, workers: map[string]*channelWorker{}}
}

func (m *Manager) Dispatch(ctx context.Context, msg feishu.IncomingMessage) error {
	if msg.ChannelKey == "" {
		msg.ChannelKey = feishu.BuildChannelKey(msg.ChatType, msg.ChatID, msg.ThreadID, msg.AppID)
	}
	worker, err := m.workerFor(msg.ChannelKey)
	if err != nil {
		return err
	}
	result := make(chan error, 1)
	job := dispatchJob{ctx: ctx, msg: msg, result: result}
	select {
	case worker.jobs <- job:
	case <-ctx.Done():
		return ctx.Err()
	default:
		return m.rejectOverflow(ctx, msg)
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	for _, worker := range m.workers {
		close(worker.stop)
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) workerFor(channelKey string) (*channelWorker, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("session manager 已关闭")
	}
	if worker := m.workers[channelKey]; worker != nil {
		return worker, nil
	}
	worker := &channelWorker{
		channelKey: channelKey,
		jobs:       make(chan dispatchJob, m.opts.QueueSize),
		stop:       make(chan struct{}),
	}
	m.workers[channelKey] = worker
	m.wg.Add(1)
	go m.runWorker(worker)
	return worker, nil
}

func (m *Manager) runWorker(worker *channelWorker) {
	defer m.wg.Done()
	timer := time.NewTimer(m.opts.WorkerIdleTimeout)
	defer timer.Stop()
	for {
		select {
		case job := <-worker.jobs:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			job.result <- m.dispatchNow(job.ctx, job.msg)
			timer.Reset(m.opts.WorkerIdleTimeout)
		case <-timer.C:
			m.mu.Lock()
			if len(worker.jobs) == 0 && m.workers[worker.channelKey] == worker {
				delete(m.workers, worker.channelKey)
				m.mu.Unlock()
				return
			}
			m.mu.Unlock()
			timer.Reset(m.opts.WorkerIdleTimeout)
		case <-worker.stop:
			for {
				select {
				case job := <-worker.jobs:
					job.result <- m.dispatchNow(job.ctx, job.msg)
				default:
					return
				}
			}
		}
	}
}

func (m *Manager) rejectOverflow(ctx context.Context, msg feishu.IncomingMessage) error {
	_ = m.store.Turns().Save(model.Turn{
		ID:         uuid.NewString(),
		AppID:      msg.AppID,
		ChannelKey: msg.ChannelKey,
		Status:     "rejected",
		Prompt:     msg.Prompt,
		ErrorKind:  "queue_overflow",
		CreatedAt:  time.Now(),
	})
	if !msg.SuppressOutput {
		_, _ = m.sender.SendText(ctx, msg.ReceiveID, msg.ReceiveType, "当前频道仍在处理上一条消息，请稍后再试")
	}
	return errors.New("channel queue overflow")
}

func (m *Manager) dispatchNow(ctx context.Context, msg feishu.IncomingMessage) error {
	if seen, err := m.store.Messages().ExistsFeishuMessage(msg.MessageID); err != nil || seen {
		return err
	}
	if err := m.opts.Guardrail.CheckInput(msg.Prompt, msg.ChatID); err != nil {
		return err
	}
	if err := m.store.Channels().Save(model.Channel{ChannelKey: msg.ChannelKey, AppID: msg.AppID, ChatType: msg.ChatType, ChatID: msg.ChatID, ThreadID: msg.ThreadID, CreatedAt: time.Now()}); err != nil {
		return err
	}
	if len(msg.Attachments) > 0 && strings.TrimSpace(msg.Prompt) == "" {
		return m.recordPendingAttachment(ctx, msg)
	}
	if strings.TrimSpace(msg.Prompt) == "/new" {
		if err := m.store.Sessions().ArchiveActive(msg.ChannelKey); err != nil {
			return err
		}
		if err := m.store.Attachments().UpdateState(msg.ChannelKey, model.AttachmentPending, model.AttachmentClearedByNew, ""); err != nil {
			return err
		}
		_, err := m.sender.SendText(ctx, msg.ReceiveID, msg.ReceiveType, "新对话从这里开始")
		return err
	}
	sess, err := m.ensureSession(msg)
	if err != nil {
		return err
	}
	mergedPrompt, err := m.consumePendingIntoPrompt(msg, sess.ID)
	if err != nil {
		return err
	}
	if err := m.store.Messages().Save(model.Message{ID: uuid.NewString(), SessionID: sess.ID, SenderID: msg.SenderID, Role: model.MessageRoleUser, Content: mergedPrompt, FeishuMsgID: msg.MessageID, CreatedAt: time.Now()}); err != nil {
		return err
	}
	startedAt := time.Now()
	m.emit(ctx, observability.Event{AppID: msg.AppID, ChannelKey: msg.ChannelKey, SessionID: sess.ID, MessageID: msg.MessageID, EventType: observability.EventTurnStarted, At: startedAt})
	enginePrompt := mergedPrompt
	if m.opts.WorkspaceDir != "" {
		writer := sessionctx.Writer{WorkspaceDir: m.opts.WorkspaceDir}
		context := sessionctx.Context{
			AppID:          msg.AppID,
			WorkspaceMode:  m.opts.WorkspaceMode,
			SessionID:      sess.ID,
			ChannelKey:     msg.ChannelKey,
			ChatType:       msg.ChatType,
			ChatID:         msg.ChatID,
			ThreadID:       msg.ThreadID,
			ReceiveID:      msg.ReceiveID,
			ReceiveType:    msg.ReceiveType,
			SenderID:       msg.SenderID,
			MessageID:      msg.MessageID,
			EngineThreadID: sess.EngineThreadID,
		}
		if _, err := writer.Write(context); err != nil {
			return err
		}
		enginePrompt = sessionctx.InjectRouting(mergedPrompt, context)
	}
	policy := engine.ThreadResumeExisting
	threadID := sess.EngineThreadID
	if m.opts.WorkspaceMode == "companion" {
		policy = engine.ThreadForceNew
		threadID = ""
	}
	var cardID string
	if m.opts.WorkspaceMode == "work" && !msg.SuppressOutput {
		cardID, _ = m.sender.SendThinking(ctx, msg.ReceiveID, msg.ReceiveType)
	}
	stream, err := m.engine.SendTurn(ctx, engine.TurnRequest{Prompt: enginePrompt, Scenario: msg.Scenario, ThreadID: threadID, ThreadPolicy: policy})
	if err != nil {
		return err
	}
	final, turn, approvals, err := m.collectTurn(stream, msg, sess.ID)
	if err != nil {
		return err
	}
	for _, req := range approvals {
		if err := m.store.Approvals().Save(req); err != nil {
			return err
		}
	}
	if err := m.opts.Guardrail.CheckOutput(final); err != nil {
		turn.Status = "failed"
		turn.ErrorKind = "output_limit"
		_ = m.store.Turns().Save(turn)
		return err
	}
	processed, err := output.Process(ctx, final, nil)
	if err != nil {
		turn.Status = "failed"
		turn.ErrorKind = "empty_output"
		_ = m.store.Turns().Save(turn)
		return err
	}
	turn.Output = processed.StoredText
	if err := m.store.Turns().Save(turn); err != nil {
		return err
	}
	eventType := observability.EventTurnCompleted
	if turn.Status != "completed" {
		eventType = observability.EventTurnFailed
	}
	m.emit(ctx, observability.Event{
		AppID:          msg.AppID,
		ChannelKey:     msg.ChannelKey,
		SessionID:      sess.ID,
		EngineThreadID: turn.EngineThreadID,
		MessageID:      msg.MessageID,
		TurnID:         turn.ID,
		EventType:      eventType,
		DurationMS:     time.Since(startedAt).Milliseconds(),
		InputTokens:    turn.InputTokens,
		OutputTokens:   turn.OutputTokens,
		ErrorKind:      turn.ErrorKind,
		At:             time.Now(),
	})
	if msg.SuppressOutput {
		if sess.EngineThreadID == "" {
			if err := m.store.Sessions().SetEngineThreadID(sess.ID, turn.EngineThreadID); err != nil {
				return err
			}
		}
		return nil
	}
	if m.opts.WorkspaceMode == "work" {
		if err := m.sender.UpdateCard(ctx, cardID, processed.StoredText); err != nil {
			return err
		}
		if sess.EngineThreadID == "" {
			if err := m.store.Sessions().SetEngineThreadID(sess.ID, turn.EngineThreadID); err != nil {
				return err
			}
		}
	} else {
		for _, segment := range processed.Segments {
			if _, err := m.sender.SendText(ctx, msg.ReceiveID, msg.ReceiveType, segment); err != nil {
				return err
			}
		}
	}
	return m.store.Messages().Save(model.Message{ID: uuid.NewString(), SessionID: sess.ID, Role: model.MessageRoleAssistant, Content: processed.StoredText, CreatedAt: time.Now()})
}

func (m *Manager) emit(ctx context.Context, ev observability.Event) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	m.opts.Emitter.Emit(ctx, ev)
}

func (m *Manager) recordPendingAttachment(ctx context.Context, msg feishu.IncomingMessage) error {
	for _, att := range msg.Attachments {
		if err := m.store.Attachments().Save(model.Attachment{ID: first(att.ID, uuid.NewString()), AppID: msg.AppID, ChannelKey: msg.ChannelKey, State: model.AttachmentPending, OriginalName: att.OriginalName, TempPath: att.TempPath, CreatedAt: time.Now()}); err != nil {
			return err
		}
	}
	_, err := m.sender.SendText(ctx, msg.ReceiveID, msg.ReceiveType, "文件已放好，直接告诉我怎么用")
	return err
}

func (m *Manager) consumePendingIntoPrompt(msg feishu.IncomingMessage, sessionID string) (string, error) {
	pending, err := m.store.Attachments().ByChannelState(msg.ChannelKey, model.AttachmentPending)
	if err != nil || len(pending) == 0 {
		return msg.Prompt, err
	}
	var refs []string
	for _, att := range pending {
		sessionPath := att.TempPath
		if m.opts.WorkspaceDir != "" {
			dir := filepath.Join(m.opts.WorkspaceDir, "sessions", sessionID, "attachments")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", err
			}
			if unsafeTempPath(att.TempPath) {
				return "", fmt.Errorf("附件临时路径非法")
			}
			// 同一条消息可能上传同名文件，使用附件 ID 前缀避免覆盖。
			sessionPath = filepath.Join(dir, feishu.SanitizeFilename(att.ID)+"-"+feishu.SanitizeFilename(att.OriginalName))
			if att.TempPath != "" {
				if err := copyFile(att.TempPath, sessionPath); err != nil {
					return "", err
				}
			}
		}
		if err := m.store.Attachments().MarkConsumed(att.ID, sessionID, sessionPath); err != nil {
			return "", err
		}
		refs = append(refs, fmt.Sprintf("[附件: %s -> %s]", att.OriginalName, sessionPath))
	}
	return msg.Prompt + "\n" + strings.Join(refs, "\n"), nil
}

func unsafeTempPath(path string) bool {
	if path == "" {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func (m *Manager) ensureSession(msg feishu.IncomingMessage) (model.Session, error) {
	if m.opts.WorkspaceMode == "work" {
		if sess, ok, err := m.store.Sessions().ActiveByChannel(msg.ChannelKey); err != nil || ok {
			return sess, err
		}
	}
	sess := model.Session{ID: uuid.NewString(), ChannelKey: msg.ChannelKey, Status: model.SessionActive, CreatedBy: msg.SenderID, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	return sess, m.store.Sessions().Save(sess)
}

func (m *Manager) collectTurn(stream engine.EventStream, msg feishu.IncomingMessage, sessionID string) (string, model.Turn, []model.ApprovalRequest, error) {
	turn := model.Turn{ID: uuid.NewString(), AppID: msg.AppID, ChannelKey: msg.ChannelKey, SessionID: sessionID, Status: "completed", Prompt: msg.Prompt, CreatedAt: time.Now()}
	var b strings.Builder
	var approvals []model.ApprovalRequest
	events := 0
	for stream.Next() {
		events++
		if err := m.opts.Guardrail.CheckEventCount(events); err != nil {
			turn.Status = "failed"
			turn.ErrorKind = "event_limit"
			_ = m.store.Turns().Save(turn)
			return "", turn, approvals, err
		}
		ev := stream.Event()
		if ev.ThreadID != "" {
			turn.EngineThreadID = ev.ThreadID
		}
		if ev.Type == engine.EventDelta || ev.Type == engine.EventCompleted {
			b.WriteString(ev.Text)
		}
		if ev.Type == engine.EventCompleted {
			turn.InputTokens = ev.InputTokens
			turn.OutputTokens = ev.OutputTokens
			now := time.Now()
			turn.CompletedAt = &now
		}
		if ev.Type == engine.EventFailed || ev.Type == engine.EventInterrupted {
			turn.Status = string(ev.Type)
			turn.ErrorKind = ev.Error
		}
		if ev.Type == engine.EventApprovalRequested {
			expires := time.Now().Add(m.opts.ApprovalTimeout)
			if m.opts.ApprovalTimeout <= 0 {
				expires = time.Now().Add(5 * time.Minute)
			}
			turn.Status = "pending_approval"
			approvals = append(approvals, model.ApprovalRequest{
				ID:             first(ev.ApprovalID, uuid.NewString()),
				AppID:          msg.AppID,
				ChannelKey:     msg.ChannelKey,
				SessionID:      sessionID,
				TurnID:         turn.ID,
				EngineThreadID: ev.ThreadID,
				Status:         "pending_user",
				RequestJSON:    ev.ApprovalJSON,
				CreatedAt:      time.Now(),
				ExpiresAt:      expires,
			})
		}
	}
	if err := stream.Err(); err != nil {
		return "", turn, approvals, err
	}
	if turn.EngineThreadID == "" {
		return "", turn, approvals, fmt.Errorf("engine thread id 为空")
	}
	return b.String(), turn, approvals, nil
}

type dispatchJob struct {
	ctx    context.Context
	msg    feishu.IncomingMessage
	result chan error
}

type channelWorker struct {
	channelKey string
	jobs       chan dispatchJob
	stop       chan struct{}
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func CleanupExpiredAttachments(store *db.Store, channelKey string, ttlSeconds int) error {
	return store.Attachments().ExpirePending(channelKey)
}
