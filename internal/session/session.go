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
	WorkspaceMode         string
	WorkspaceDir          string
	Guardrail             guardrail.Guardrail
	QueueSize             int
	WorkerIdleTimeout     time.Duration
	DuplicateMessageTTL   time.Duration
	ApprovalTimeout       time.Duration
	PendingAttachmentTTL  time.Duration
	AttachmentTempDir     string
	MaxPendingAttachments int
	MaxAttachmentBytes    int64
	Emitter               observability.Emitter
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
	ctx     context.Context
	cancel  context.CancelFunc
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
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{store: store, engine: eng, sender: sender, opts: opts, workers: map[string]*channelWorker{}, ctx: ctx, cancel: cancel}
}

func (m *Manager) Dispatch(ctx context.Context, msg feishu.IncomingMessage) error {
	if msg.ChannelKey == "" {
		msg.ChannelKey = feishu.BuildChannelKey(msg.ChatType, msg.ChatID, msg.ThreadID, msg.AppID)
	}
	result := make(chan error, 1)
	job := dispatchJob{ctx: ctx, msg: msg, result: result}
	err := m.enqueue(ctx, msg.ChannelKey, job)
	if err != nil {
		return err
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
	m.cancel()
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

func (m *Manager) enqueue(ctx context.Context, channelKey string, job dispatchJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("session manager 已关闭")
	}
	worker := m.workers[channelKey]
	if worker == nil {
		worker = &channelWorker{
			channelKey: channelKey,
			jobs:       make(chan dispatchJob, m.opts.QueueSize),
			stop:       make(chan struct{}),
		}
		m.workers[channelKey] = worker
		m.wg.Add(1)
		go m.runWorker(worker)
	}
	select {
	case worker.jobs <- job:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return m.rejectOverflow(ctx, job.msg)
	}
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
			ctx, cancel := m.jobContext(job.ctx)
			job.result <- m.dispatchNow(ctx, job.msg)
			cancel()
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
					ctx, cancel := m.jobContext(job.ctx)
					job.result <- m.dispatchNow(ctx, job.msg)
					cancel()
				default:
					return
				}
			}
		}
	}
}

func (m *Manager) jobContext(ctx context.Context) (context.Context, context.CancelFunc) {
	jobCtx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-ctx.Done():
		case <-m.ctx.Done():
			cancel()
		case <-jobCtx.Done():
		}
	}()
	return jobCtx, cancel
}

func (m *Manager) rejectOverflow(ctx context.Context, msg feishu.IncomingMessage) error {
	turn := model.Turn{
		ID:         uuid.NewString(),
		AppID:      msg.AppID,
		ChannelKey: msg.ChannelKey,
		Status:     "rejected",
		Prompt:     msg.Prompt,
		ErrorKind:  "queue_overflow",
		CreatedAt:  time.Now(),
	}
	_ = m.store.Turns().Save(turn)
	m.emit(ctx, observability.Event{
		AppID:      msg.AppID,
		ChannelKey: msg.ChannelKey,
		MessageID:  msg.MessageID,
		TurnID:     turn.ID,
		EventType:  observability.EventDispatchRejected,
		ErrorKind:  "queue_overflow",
		At:         time.Now(),
	})
	if !msg.SuppressOutput {
		_, _ = m.sender.SendText(ctx, msg.ReceiveID, msg.ReceiveType, "当前频道仍在处理上一条消息，请稍后再试")
	}
	return errors.New("channel queue overflow")
}

func (m *Manager) dispatchNow(ctx context.Context, msg feishu.IncomingMessage) error {
	if msg.AppID != "" {
		if err := m.store.Approvals().ExpirePendingBefore(msg.AppID, time.Now()); err != nil {
			return err
		}
	}
	if m.opts.DuplicateMessageTTL > 0 {
		if err := m.store.EventReceipts().PruneBefore(time.Now().Add(-m.opts.DuplicateMessageTTL)); err != nil {
			return err
		}
	}
	if seen, err := m.store.EventReceipts().Seen(msg.MessageID); err != nil || seen {
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
		if err := m.recordEventReceipt(msg); err != nil {
			return err
		}
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
	if err := m.recordEventReceipt(msg); err != nil {
		return err
	}
	startedAt := time.Now()
	m.emit(ctx, observability.Event{AppID: msg.AppID, ChannelKey: msg.ChannelKey, SessionID: sess.ID, MessageID: msg.MessageID, TaskID: msg.TaskID, EventType: observability.EventTurnStarted, At: startedAt})
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
			TaskID:         msg.TaskID,
			TaskName:       msg.TaskName,
			EngineThreadID: sess.EngineThreadID,
		}
		if _, err := writer.Write(context); err != nil {
			return err
		}
		enginePrompt = sessionctx.InjectRouting(mergedPrompt, context)
	}
	policy := engine.ThreadResumeExisting
	threadID := sess.EngineThreadID
	if m.opts.WorkspaceMode == "companion" || msg.ForceNewThread {
		policy = engine.ThreadForceNew
		threadID = ""
	}
	var cardID string
	cardReady := false
	if m.opts.WorkspaceMode == "work" && !msg.SuppressOutput {
		var err error
		cardID, err = m.sender.SendThinking(ctx, msg.ReceiveID, msg.ReceiveType)
		cardReady = err == nil && cardID != ""
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
	if turn.Status == "pending_approval" {
		turn.Output = ""
		if err := m.store.Turns().Save(turn); err != nil {
			return err
		}
		return fmt.Errorf("turn pending approval")
	}
	if turn.Status != "completed" {
		turn.Output = final
		if err := m.store.Turns().Save(turn); err != nil {
			return err
		}
		if !msg.SuppressOutput && m.opts.WorkspaceMode == "work" && cardReady {
			_ = m.sender.UpdateCard(ctx, cardID, "执行失败: "+first(turn.ErrorKind, turn.Status))
		}
		m.emit(ctx, observability.Event{
			AppID: msg.AppID, ChannelKey: msg.ChannelKey, SessionID: sess.ID, EngineThreadID: turn.EngineThreadID,
			MessageID: msg.MessageID, TaskID: msg.TaskID, TurnID: turn.ID, EventType: observability.EventTurnFailed,
			DurationMS: time.Since(startedAt).Milliseconds(), ErrorKind: turn.ErrorKind, At: time.Now(),
		})
		return fmt.Errorf("engine turn failed: %s", first(turn.ErrorKind, turn.Status))
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
		TaskID:         msg.TaskID,
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
		if cardReady {
			if err := m.sender.UpdateCard(ctx, cardID, processed.StoredText); err != nil {
				if _, fallbackErr := m.sender.SendText(ctx, msg.ReceiveID, msg.ReceiveType, processed.StoredText); fallbackErr != nil {
					return err
				}
			}
		} else {
			if _, err := m.sender.SendText(ctx, msg.ReceiveID, msg.ReceiveType, processed.StoredText); err != nil {
				return err
			}
		}
		if sess.EngineThreadID == "" {
			if err := m.store.Sessions().SetEngineThreadID(sess.ID, turn.EngineThreadID); err != nil {
				return err
			}
		}
	} else {
		for _, segment := range processed.Segments {
			if _, err := m.sender.SendText(ctx, msg.ReceiveID, msg.ReceiveType, segment); err != nil {
				if strings.Contains(err.Error(), "99991400") {
					if _, retryErr := m.sender.SendText(ctx, msg.ReceiveID, msg.ReceiveType, segment); retryErr == nil {
						continue
					}
				}
				continue
			}
		}
	}
	return m.store.Messages().Save(model.Message{ID: uuid.NewString(), SessionID: sess.ID, Role: model.MessageRoleAssistant, Content: processed.StoredText, CreatedAt: time.Now()})
}

func (m *Manager) recordEventReceipt(msg feishu.IncomingMessage) error {
	return m.store.EventReceipts().Save(msg.MessageID, msg.AppID)
}

func (m *Manager) emit(ctx context.Context, ev observability.Event) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	m.opts.Emitter.Emit(ctx, ev)
}

func (m *Manager) recordPendingAttachment(ctx context.Context, msg feishu.IncomingMessage) error {
	if m.opts.MaxPendingAttachments > 0 {
		existing, err := m.store.Attachments().CountByChannelState(msg.ChannelKey, model.AttachmentPending)
		if err != nil {
			return err
		}
		if existing+int64(len(msg.Attachments)) > int64(m.opts.MaxPendingAttachments) {
			return fmt.Errorf("pending attachments limit exceeded")
		}
	}
	for _, att := range msg.Attachments {
		if unsafeTempPath(att.TempPath, m.opts.AttachmentTempDir) {
			return fmt.Errorf("附件临时路径非法")
		}
		if m.opts.MaxAttachmentBytes > 0 && att.SizeBytes > m.opts.MaxAttachmentBytes {
			return fmt.Errorf("attachment size limit exceeded")
		}
		if err := m.store.Attachments().Save(model.Attachment{ID: first(att.ID, uuid.NewString()), AppID: msg.AppID, ChannelKey: msg.ChannelKey, State: model.AttachmentPending, OriginalName: att.OriginalName, TempPath: att.TempPath, SourceMsgID: msg.MessageID, CreatedAt: time.Now()}); err != nil {
			return err
		}
	}
	if err := m.recordEventReceipt(msg); err != nil {
		return err
	}
	_, err := m.sender.SendText(ctx, msg.ReceiveID, msg.ReceiveType, "文件已放好，直接告诉我怎么用")
	return err
}

func (m *Manager) consumePendingIntoPrompt(msg feishu.IncomingMessage, sessionID string) (string, error) {
	if m.opts.PendingAttachmentTTL > 0 {
		if err := CleanupExpiredAttachmentsWithRoots(m.store, msg.ChannelKey, int(m.opts.PendingAttachmentTTL.Seconds()), m.opts.WorkspaceDir, m.opts.AttachmentTempDir); err != nil {
			return "", err
		}
	}
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
			if unsafeTempPath(att.TempPath, m.opts.AttachmentTempDir) {
				return "", fmt.Errorf("附件临时路径非法")
			}
			if m.opts.MaxAttachmentBytes > 0 && att.TempPath != "" {
				info, err := os.Stat(att.TempPath)
				if err != nil {
					return "", err
				}
				if info.Size() > m.opts.MaxAttachmentBytes {
					return "", fmt.Errorf("attachment size limit exceeded")
				}
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

func unsafeTempPath(path string, roots ...string) bool {
	if path == "" {
		return false
	}
	clean, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return true
	}
	if info, err := os.Lstat(clean); err != nil || info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	for _, allowed := range roots {
		if allowed == "" {
			continue
		}
		allowedAbs, err := filepath.Abs(allowed)
		if err != nil {
			continue
		}
		allowedResolved, err := filepath.EvalSymlinks(allowedAbs)
		if err != nil {
			allowedResolved = allowedAbs
		}
		if resolved == allowedResolved || strings.HasPrefix(resolved, allowedResolved+string(os.PathSeparator)) {
			return false
		}
	}
	return true
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if info, err := in.Stat(); err == nil && info.Size() < 0 {
		return fmt.Errorf("附件大小非法")
	}
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
	turn := model.Turn{ID: uuid.NewString(), AppID: msg.AppID, ChannelKey: msg.ChannelKey, SessionID: sessionID, TaskID: msg.TaskID, Status: "completed", Prompt: msg.Prompt, CreatedAt: time.Now()}
	var b strings.Builder
	var approvals []model.ApprovalRequest
	events := 0
	var collected []engine.TurnEvent
	approvalRequested := false
	for stream.Next() {
		events++
		if err := m.opts.Guardrail.CheckEventCount(events); err != nil {
			turn.Status = "failed"
			turn.ErrorKind = "event_limit"
			_ = m.store.Turns().Save(turn)
			return "", turn, approvals, err
		}
		ev := stream.Event()
		collected = append(collected, ev)
		if ev.ThreadID != "" {
			turn.EngineThreadID = ev.ThreadID
		}
		if !approvalRequested && (ev.Type == engine.EventDelta || ev.Type == engine.EventCompleted) {
			b.WriteString(ev.Text)
		}
		if ev.Type == engine.EventCompleted {
			turn.InputTokens = ev.InputTokens
			turn.OutputTokens = ev.OutputTokens
			now := time.Now()
			turn.CompletedAt = &now
		}
		if !approvalRequested && (ev.Type == engine.EventFailed || ev.Type == engine.EventInterrupted) {
			turn.Status = string(ev.Type)
			turn.ErrorKind = ev.Error
		}
		if ev.Type == engine.EventApprovalRequested {
			approvalRequested = true
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
	if err := engine.ValidateEvents(collected); err != nil {
		turn.Status = "failed"
		turn.ErrorKind = "invalid_event_sequence"
		_ = m.store.Turns().Save(turn)
		return "", turn, approvals, err
	}
	if turn.EngineThreadID == "" {
		return "", turn, approvals, fmt.Errorf("engine thread id 为空")
	}
	if approvalRequested {
		turn.Status = "pending_approval"
		turn.ErrorKind = ""
		turn.Output = ""
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
	return CleanupExpiredAttachmentsWithRoots(store, channelKey, ttlSeconds, filepath.Dir(store.Path()))
}

func CleanupExpiredAttachmentsWithRoots(store *db.Store, channelKey string, ttlSeconds int, roots ...string) error {
	cutoff := time.Now()
	if ttlSeconds > 0 {
		cutoff = cutoff.Add(-time.Duration(ttlSeconds) * time.Second)
	}
	expired, err := store.Attachments().ExpirePendingBefore(channelKey, cutoff)
	if err != nil {
		return err
	}
	for _, att := range expired {
		for _, path := range []string{att.TempPath, att.SessionPath} {
			if path == "" {
				continue
			}
			if unsafeTempPath(path, roots...) {
				continue
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}
