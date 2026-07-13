package worker

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	presentation "github.com/kid0317/codex-workspace-bot/internal/output"
)

var (
	ErrQueueFull     = errors.New("worker queue full")
	ErrPoolSaturated = errors.New("worker pool saturated")
	ErrClosed        = errors.New("worker manager closed")
	ErrStopping      = errors.New("worker stopping")
	ErrWorkflowTrace = errors.New("companion_delivery_trace_incomplete")
)

type State string

const (
	Idle         State = "idle"
	CreatingCard State = "creating_card"
	InProcess    State = "in_process"
	Stopping     State = "stopping"
	Stopped      State = "stopped"
)

type Key struct {
	Kind  string
	Peer  string
	AppID string
}

func GroupKey(chatID, appID string) Key { return Key{Kind: "group", Peer: chatID, AppID: appID} }
func P2PKey(openID, appID string) Key   { return Key{Kind: "p2p", Peer: openID, AppID: appID} }
func (k Key) String() string            { return k.Kind + ":" + k.Peer + ":" + k.AppID }

type ReplyTarget struct{ ID, Type string }

type AppRuntime struct {
	ID, WorkspaceDir, WorkspaceMode, Model, Effort string
}

type Message struct {
	ID, TraceID, ChatGroupID string
	Key                      Key
	Runtime                  AppRuntime
	Reply                    ReplyTarget
	Query                    string
	ReceivedAt               time.Time
	// HasRequiredAttachment makes this message an exclusive FIFO batch. The
	// owning worker must materialize its attachment before it can start a turn.
	HasRequiredAttachment bool
	AttachmentIDs         []string
	// AttachmentOutboxDir is assigned only by the attachment resolver for the
	// active attachment batch. It is never persisted or logged.
	AttachmentOutboxDir string
}

type Batch struct {
	ID       string
	Key      Key
	Runtime  AppRuntime
	Messages []Message
	OnItem   func(PresentationItem) bool
}

type PresentationItem = presentation.Item

type DualZoneOutput interface {
	UpdateBatchCardZones(context.Context, string, string, string, string, bool) error
}

func (b Batch) Queries() []string {
	queries := make([]string, len(b.Messages))
	for i, message := range b.Messages {
		queries[i] = message.Query
	}
	return queries
}

type Output interface {
	CreateBatchCard(context.Context, Batch) (string, error)
	UpdateBatchCard(context.Context, string, string) error
	SendBatchText(context.Context, ReplyTarget, string) (string, error)
}

type CompanionSendOutcome string

const (
	CompanionSent      CompanionSendOutcome = "sent"
	CompanionRejected  CompanionSendOutcome = "rejected"
	CompanionUnknown   CompanionSendOutcome = "unknown"
	CompanionCancelled CompanionSendOutcome = "cancelled"
)

type CompanionSendResult struct {
	MessageID string
	Outcome   CompanionSendOutcome
	Reason    string
}

// CompanionWorkflowEvent contains delivery metadata only. It intentionally
// excludes user and assistant text; TextSHA256 is sufficient for correlation.
type CompanionWorkflowEvent struct {
	BatchID        string
	SourceTraceIDs []string
	ThreadID       string
	TurnID         string
	SegmentIndex   int
	TextSHA256     string
	Result         CompanionSendOutcome
	Reason         string
	MessageID      string
	RetryCount     int
	At             time.Time
}

type WorkflowWriter interface {
	WriteCompanionSegment(context.Context, CompanionWorkflowEvent) error
}

type WorkflowWriterFunc func(context.Context, CompanionWorkflowEvent) error

func (f WorkflowWriterFunc) WriteCompanionSegment(ctx context.Context, event CompanionWorkflowEvent) error {
	return f(ctx, event)
}

// CompanionSegmentSender is optional so existing non-companion adapters keep
// the small Output boundary. Companion adapters must classify network-unknown
// sends separately from explicit rejections to avoid unsafe replay.
type CompanionSegmentSender interface {
	SendCompanionSegment(context.Context, ReplyTarget, string) CompanionSendResult
}

type OutputForBatch func(Batch) (Output, error)
type ProcessResult struct {
	// DurationMS is measured by the Codex adapter from accepted turn/start to its terminal outcome.
	DurationMS       int64
	ThreadID, TurnID string
}
type Processor func(context.Context, Batch) (ProcessResult, error)

type Lifecycle interface {
	MarkProcessing(context.Context, []string) error
	Complete(context.Context, []string, string, string, int64) error
	Fail(context.Context, []string, string, int64) error
}

type CompanionDeliverySummary struct {
	BatchID, FirstMessageID, Content, ErrorCode string
	DurationMS                                  int64
}

type CompanionLifecycle interface {
	MarkCompanionDeliveryStarted(context.Context, []string, string) error
	CompleteCompanionDelivery(context.Context, []string, CompanionDeliverySummary) error
	FailCompanionDelivery(context.Context, []string, string, string, string, int64) error
}

type Config struct {
	MaxWorkers            int
	QueueDepth            int
	ProcessTimeout        time.Duration
	IdleTimeout           time.Duration
	StopGrace             time.Duration
	CompanionSegmentDelay time.Duration
	WorkflowLogger        *slog.Logger
	WorkflowWriter        WorkflowWriter
}

type Manager struct {
	cfg       Config
	outputFor OutputForBatch
	processor Processor
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	workers   map[string]*channelWorker
	sequence  atomic.Uint64
	lifecycle Lifecycle
}

func NewManager(cfg Config, outputFor OutputForBatch, processor Processor, lifecycle ...Lifecycle) *Manager {
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 20
	}
	if cfg.QueueDepth <= 0 {
		cfg.QueueDepth = 64
	}
	if cfg.ProcessTimeout <= 0 {
		cfg.ProcessTimeout = 90 * time.Minute
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 30 * time.Minute
	}
	if cfg.StopGrace <= 0 {
		cfg.StopGrace = 10 * time.Second
	}
	if cfg.CompanionSegmentDelay <= 0 {
		cfg.CompanionSegmentDelay = 400 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	var observer Lifecycle
	if len(lifecycle) > 0 {
		observer = lifecycle[0]
	}
	return &Manager{cfg: cfg, outputFor: outputFor, processor: processor, lifecycle: observer, ctx: ctx, cancel: cancel, workers: make(map[string]*channelWorker)}
}

func (m *Manager) Accept(_ context.Context, message Message) error {
	key := message.Key.String()
	m.mu.Lock()
	if m.ctx.Err() != nil {
		m.mu.Unlock()
		return ErrClosed
	}
	w := m.workers[key]
	if w == nil {
		if len(m.workers) >= m.cfg.MaxWorkers {
			m.mu.Unlock()
			return ErrPoolSaturated
		}
		w = &channelWorker{manager: m, key: message.Key, state: Idle, idleAt: time.Now(), wake: make(chan struct{}, 1), stop: make(chan struct{})}
		m.workers[key] = w
		go w.run(m.ctx)
	}
	m.mu.Unlock()
	return w.enqueue(message)
}

func (m *Manager) Close() { m.cancel() }

// Cancel serializes a control command with the channel worker. It does not
// remove already-visible Feishu messages; it cancels the active turn or the
// companion delivery context and waits for that batch to release the channel.
func (m *Manager) Cancel(ctx context.Context, key Key) error {
	m.mu.Lock()
	w := m.workers[key.String()]
	m.mu.Unlock()
	if w == nil {
		return nil
	}
	w.mu.Lock()
	cancel, done := w.activeCancel, w.activeDone
	terminal, delivery := w.terminal, w.delivery
	w.activeCancelled = true
	if terminal != nil {
		terminal.Claim("cancelled")
	}
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if delivery != nil {
		if err := delivery.CancelAndWait(ctx); err != nil {
			return err
		}
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) State(key Key) (State, bool) {
	m.mu.Lock()
	w := m.workers[key.String()]
	m.mu.Unlock()
	if w == nil {
		return "", false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state, true
}

type channelWorker struct {
	manager         *Manager
	key             Key
	mu              sync.Mutex
	queue           []Message
	processing      bool
	state           State
	idleAt          time.Time
	activeCancel    context.CancelFunc
	activeDone      chan struct{}
	activeCancelled bool
	terminal        *TerminalArbiter
	delivery        *DeliverySlot
	done            chan struct{}
	stop            chan struct{}
	wake            chan struct{}
}

func (w *channelWorker) setActive(cancel context.CancelFunc) {
	w.mu.Lock()
	w.activeCancel = cancel
	w.activeDone = make(chan struct{})
	w.activeCancelled = false
	w.mu.Unlock()
}

func (w *channelWorker) replaceActive(cancel context.CancelFunc) {
	w.mu.Lock()
	w.activeCancel = cancel
	cancelNow := w.activeCancelled
	w.mu.Unlock()
	if cancelNow {
		cancel()
	}
}

func (w *channelWorker) clearActive() {
	w.mu.Lock()
	done := w.activeDone
	w.activeCancel, w.activeDone = nil, nil
	w.activeCancelled = false
	w.mu.Unlock()
	if done != nil {
		close(done)
	}
}

func (w *channelWorker) setCompanionControl(terminal *TerminalArbiter, delivery *DeliverySlot) {
	w.mu.Lock()
	w.terminal, w.delivery = terminal, delivery
	w.mu.Unlock()
}

func (w *channelWorker) clearCompanionControl() {
	w.mu.Lock()
	w.terminal, w.delivery = nil, nil
	w.mu.Unlock()
}

func (w *channelWorker) enqueue(message Message) error {
	w.mu.Lock()
	if w.state == Stopping || w.state == Stopped {
		w.mu.Unlock()
		return ErrStopping
	}
	if len(w.queue) >= w.manager.cfg.QueueDepth {
		w.mu.Unlock()
		return ErrQueueFull
	}
	w.queue = append(w.queue, message)
	w.mu.Unlock()
	w.signal()
	return nil
}

func (w *channelWorker) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *channelWorker) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case <-w.wake:
		}
		for {
			batch, ok := w.nextBatch()
			if !ok {
				break
			}
			w.process(ctx, batch)
		}
	}
}

func (w *channelWorker) nextBatch() (Batch, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.processing || len(w.queue) == 0 {
		return Batch{}, false
	}
	batchSize := len(w.queue)
	for index, message := range w.queue {
		if !message.HasRequiredAttachment {
			continue
		}
		if index == 0 {
			batchSize = 1
		} else {
			batchSize = index
		}
		break
	}
	messages := append([]Message(nil), w.queue[:batchSize]...)
	w.queue = w.queue[batchSize:]
	w.processing = true
	w.state = CreatingCard
	id := uuid.NewString()
	return Batch{ID: id, Key: w.key, Runtime: messages[0].Runtime, Messages: messages}, true
}

func (w *channelWorker) process(parent context.Context, batch Batch) {
	messageIDs := make([]string, len(batch.Messages))
	for i, message := range batch.Messages {
		messageIDs[i] = message.ID
	}
	output, err := w.manager.outputFor(batch)
	if batch.Runtime.WorkspaceMode == "companion" {
		w.processCompanion(parent, batch, messageIDs, output, err)
		return
	}
	if err == nil && output != nil {
		cardID, createErr := output.CreateBatchCard(parent, batch)
		if createErr == nil {
			var projection *presentation.Projection
			var drainPresentation func() error
			if zoneOutput, ok := output.(DualZoneOutput); ok {
				mapper := presentation.NewMapper()
				projection = presentation.NewProjection()
				type queuedPresentation struct {
					item  PresentationItem
					bytes int64
				}
				items := make(chan queuedPresentation, 64)
				var reserved atomic.Int64
				var presentationFailed atomic.Bool
				pumpDone := make(chan struct{})
				go func() {
					defer close(pumpDone)
					ticker := time.NewTicker(250 * time.Millisecond)
					defer ticker.Stop()
					dirty := false
					flush := func() {
						if !dirty || presentationFailed.Load() {
							return
						}
						if err := zoneOutput.UpdateBatchCardZones(parent, cardID, projection.Final(), projection.Progress(), "生成中…", false); err != nil {
							presentationFailed.Store(true)
							return
						}
						dirty = false
					}
					for {
						select {
						case queued, open := <-items:
							if !open {
								flush()
								return
							}
							mapped, visible := mapper.Accept(queued.item)
							reserved.Add(-queued.bytes)
							if !visible || presentationFailed.Load() {
								continue
							}
							projection.Apply(mapped)
							dirty = true
							if mapped.Phase == "final_answer" {
								flush()
							}
						case <-ticker.C:
							flush()
						}
					}
				}()
				batch.OnItem = func(item PresentationItem) bool {
					if presentationFailed.Load() {
						return false
					}
					size := int64(len(item.Text))
					for {
						used := reserved.Load()
						if size > 256*1024 || used+size > 256*1024 {
							return false
						}
						if reserved.CompareAndSwap(used, used+size) {
							break
						}
					}
					select {
					case items <- queuedPresentation{item: item, bytes: size}:
						return true
					default:
						reserved.Add(-size)
						return false
					}
				}
				drainPresentation = func() error {
					close(items)
					<-pumpDone
					if presentationFailed.Load() {
						return errors.New("presentation delivery failed")
					}
					return nil
				}
			}
			slog.Info("batch_card_created", "batch_id", batch.ID, "channel_key", batch.Key.String(), "batch_size", len(batch.Messages))
			if w.manager.lifecycle != nil {
				_ = w.manager.lifecycle.MarkProcessing(parent, messageIDs)
			}
			w.mu.Lock()
			w.state = InProcess
			w.mu.Unlock()
			slog.Info("batch_started", "batch_id", batch.ID, "channel_key", batch.Key.String(), "batch_size", len(batch.Messages))
			ctx, cancel := context.WithTimeout(parent, w.manager.cfg.ProcessTimeout)
			w.setActive(cancel)
			defer w.clearActive()
			result := make(chan struct {
				result ProcessResult
				err    error
			}, 1)
			go func() {
				processResult, processErr := w.manager.processor(ctx, batch)
				result <- struct {
					result ProcessResult
					err    error
				}{processResult, processErr}
			}()
			processResult := ProcessResult{}
			select {
			case completed := <-result:
				processResult, err = completed.result, completed.err
			case <-ctx.Done():
				err = ctx.Err()
				w.mu.Lock()
				w.state = Stopping
				w.mu.Unlock()
				select {
				case completed := <-result:
					processResult, err = completed.result, completed.err
				case <-time.After(w.manager.cfg.StopGrace):
				}
			}
			cancel()
			if drainPresentation != nil {
				if drainErr := drainPresentation(); drainErr != nil && err == nil {
					err = drainErr
				}
			}
			durationMS := processResult.DurationMS
			processorFailed := err != nil
			content := "本轮已完成。"
			if projection != nil && projection.Final() != "" {
				content = projection.Final()
			}
			if errors.Is(err, context.DeadlineExceeded) {
				if updateErr := output.UpdateBatchCard(parent, cardID, "本批处理超时，请重新发送。"); updateErr != nil {
					_, _ = output.SendBatchText(parent, batch.Messages[0].Reply, "本批处理超时，请重新发送。")
				}
				if w.manager.lifecycle != nil {
					_ = w.manager.lifecycle.Fail(parent, messageIDs, "worker_timeout_stopped", durationMS)
				}
			} else if err != nil {
				slog.Error("batch_processor_failed", "event", "batch_processor_failed", "batch_id", batch.ID, "channel_key", batch.Key.String(), "error", err)
				failure := "本批处理失败，请重新发送。"
				if updateErr := output.UpdateBatchCard(parent, cardID, failure); updateErr != nil {
					_, _ = output.SendBatchText(parent, batch.Messages[0].Reply, failure)
				}
				if w.manager.lifecycle != nil {
					_ = w.manager.lifecycle.Fail(parent, messageIDs, terminalErrorCode(err), durationMS)
				}
			} else {
				var updateErr error
				if zoneOutput, ok := output.(DualZoneOutput); ok && projection != nil {
					updateErr = zoneOutput.UpdateBatchCardZones(parent, cardID, content, projection.Progress(), "已完成", true)
				} else {
					updateErr = output.UpdateBatchCard(parent, cardID, content)
				}
				if updateErr != nil {
					err = updateErr
				}
			}
			if err == nil {
				if w.manager.lifecycle != nil {
					_ = w.manager.lifecycle.Complete(parent, messageIDs, cardID, content, durationMS)
				}
				slog.Info("batch_completed", "batch_id", batch.ID, "channel_key", batch.Key.String(), "batch_size", len(batch.Messages))
			} else if !processorFailed && !errors.Is(err, context.DeadlineExceeded) && w.manager.lifecycle != nil && err != nil {
				slog.Error("batch_card_update_failed", "batch_id", batch.ID, "channel_key", batch.Key.String(), "error", err)
				if fallbackID, fallbackErr := output.SendBatchText(parent, batch.Messages[0].Reply, content); fallbackErr == nil {
					_ = w.manager.lifecycle.Complete(parent, messageIDs, fallbackID, content, durationMS)
				} else {
					_ = w.manager.lifecycle.Fail(parent, messageIDs, "card_update_failed", durationMS)
				}
			}
		} else if w.manager.lifecycle != nil {
			slog.Error("batch_card_create_failed", "batch_id", batch.ID, "channel_key", batch.Key.String(), "error", createErr)
			content := "本批处理失败，请重新发送。"
			_ = w.manager.lifecycle.MarkProcessing(parent, messageIDs)
			_, _ = output.SendBatchText(parent, batch.Messages[0].Reply, content)
			_ = w.manager.lifecycle.Fail(parent, messageIDs, "batch_card_create_failed", 0)
		}
	} else if w.manager.lifecycle != nil {
		_ = w.manager.lifecycle.Fail(parent, messageIDs, "output_unavailable", 0)
	}
	timedOut := errors.Is(err, context.DeadlineExceeded)
	w.mu.Lock()
	w.processing = false
	if timedOut {
		w.state = Stopped
		pending := append([]Message(nil), w.queue...)
		w.queue = nil
		w.mu.Unlock()
		if len(pending) > 0 && output != nil {
			for _, message := range pending {
				_, _ = output.SendBatchText(parent, message.Reply, "上一批处理超时，未处理消息请重新发送。")
				if w.manager.lifecycle != nil {
					_ = w.manager.lifecycle.Fail(parent, []string{message.ID}, "worker_timeout_stopped", 0)
				}
			}
		}
		w.removeFromManager()
		return
	}
	w.state = Idle
	w.idleAt = time.Now()
	hasMore := len(w.queue) > 0
	w.mu.Unlock()
	if hasMore {
		w.signal()
	} else {
		w.scheduleIdleRecycle()
	}
}

func terminalErrorCode(err error) string {
	if err != nil && err.Error() == "presentation_backpressure" {
		return "presentation_backpressure"
	}
	return "worker_process_failed"
}

func (w *channelWorker) processCompanion(parent context.Context, batch Batch, messageIDs []string, output Output, err error) {
	terminal := NewTerminalArbiter()
	delivery := NewDeliverySlot()
	w.setCompanionControl(terminal, delivery)
	defer w.clearCompanionControl()
	defer delivery.Finish()
	if err == nil && output != nil {
		mapper := presentation.NewMapper()
		projection := presentation.NewProjection()
		batch.OnItem = func(item PresentationItem) bool {
			mapped, visible := mapper.Accept(item)
			if visible {
				projection.Apply(mapped)
			}
			return true
		}
		if w.manager.lifecycle != nil {
			_ = w.manager.lifecycle.MarkProcessing(parent, messageIDs)
		}
		w.mu.Lock()
		w.state = InProcess
		w.mu.Unlock()
		ctx, cancel := context.WithTimeout(parent, w.manager.cfg.ProcessTimeout)
		w.setActive(cancel)
		defer w.clearActive()
		result := make(chan struct {
			result ProcessResult
			err    error
		}, 1)
		go func() {
			processResult, processErr := w.manager.processor(ctx, batch)
			result <- struct {
				result ProcessResult
				err    error
			}{processResult, processErr}
		}()
		processResult := ProcessResult{}
		select {
		case completed := <-result:
			processResult, err = completed.result, completed.err
		case <-ctx.Done():
			err = ctx.Err()
			w.mu.Lock()
			w.state = Stopping
			w.mu.Unlock()
			select {
			case completed := <-result:
				processResult, err = completed.result, completed.err
			case <-time.After(w.manager.cfg.StopGrace):
			}
		}
		if terminal.Reason() == "cancelled" {
			err = context.Canceled
		} else if err != nil {
			terminal.Claim("processor_failed")
		}
		cancel()
		durationMS := processResult.DurationMS
		content := projection.Final()
		companionLifecycle, hasCompanionLifecycle := w.manager.lifecycle.(CompanionLifecycle)
		deliveryCtx := parent
		markedDelivery := false
		if errors.Is(err, context.DeadlineExceeded) {
			_, _ = output.SendBatchText(deliveryCtx, batch.Messages[0].Reply, "本批处理超时，请重新发送。")
			if w.manager.lifecycle != nil {
				_ = w.manager.lifecycle.Fail(deliveryCtx, messageIDs, "worker_timeout_stopped", durationMS)
			}
		} else if err == nil {
			lexed, lexErr := presentation.LexCompanion(content)
			if lexErr != nil {
				err = lexErr
			} else {
				segments := presentation.SplitCompanion(lexed.SegmenterInput, lexed.Delimiter)
				emptyFinal := false
				if len(segments) == 0 {
					content = "本轮完成，但未收到可展示的最终答复。"
					segments = []string{content}
					lexed.StorageText = content
					emptyFinal = true
				}
				if hasCompanionLifecycle {
					if markerErr := companionLifecycle.MarkCompanionDeliveryStarted(deliveryCtx, messageIDs, batch.ID); markerErr != nil {
						err = markerErr
					} else {
						markedDelivery = delivery.Begin()
					}
				}
				if markedDelivery {
					var published bool
					deliveryCtx, published = delivery.Publish(parent)
					if !published {
						err = context.Canceled
					}
				}
				var firstMessageID string
				var sent, rejected, unknown, cancelled int
				for index, segment := range segments {
					if err != nil {
						break
					}
					if index > 0 {
						select {
						case <-deliveryCtx.Done():
							err = deliveryCtx.Err()
							cancelled++
							continue
						case <-time.After(w.manager.cfg.CompanionSegmentDelay):
						}
					}
					result := sendCompanionSegment(deliveryCtx, output, batch.Messages[0].Reply, segment)
					if result.Outcome == CompanionRejected && result.Reason == "rate_limited" {
						if recordErr := w.recordCompanionSegment(deliveryCtx, batch, processResult, index, segment, result, 0); recordErr != nil {
							terminal.Claim("workflow_trace_failed")
							err = fmt.Errorf("%w: %v", ErrWorkflowTrace, recordErr)
							break
						}
						select {
						case <-deliveryCtx.Done():
							result = CompanionSendResult{Outcome: CompanionCancelled, Reason: "cancelled"}
						case <-time.After(500 * time.Millisecond):
							result = sendCompanionSegment(deliveryCtx, output, batch.Messages[0].Reply, segment)
						}
						if recordErr := w.recordCompanionSegment(deliveryCtx, batch, processResult, index, segment, result, 1); recordErr != nil {
							terminal.Claim("workflow_trace_failed")
							err = fmt.Errorf("%w: %v", ErrWorkflowTrace, recordErr)
							break
						}
					} else {
						if recordErr := w.recordCompanionSegment(deliveryCtx, batch, processResult, index, segment, result, 0); recordErr != nil {
							terminal.Claim("workflow_trace_failed")
							err = fmt.Errorf("%w: %v", ErrWorkflowTrace, recordErr)
							break
						}
					}
					switch result.Outcome {
					case CompanionSent:
						sent++
						if firstMessageID == "" {
							firstMessageID = result.MessageID
						}
					case CompanionRejected:
						rejected++ // explicit rejection cannot have been visible; continue ordered delivery.
					case CompanionUnknown:
						unknown++
						terminal.Claim("unknown_send")
						err = errors.New("companion_segment_delivery_unknown")
					case CompanionCancelled:
						cancelled++
						terminal.Claim("cancelled")
						err = context.Canceled
					}
				}
				if err == nil && markedDelivery && terminal.Reason() == "" {
					code := ""
					if emptyFinal {
						code = "companion_final_empty"
					} else if rejected > 0 && sent == 0 {
						code = "companion_segment_delivery_none"
					} else if rejected > 0 {
						code = "companion_segment_delivery_partial"
					}
					err = companionLifecycle.CompleteCompanionDelivery(deliveryCtx, messageIDs, CompanionDeliverySummary{BatchID: batch.ID, FirstMessageID: firstMessageID, Content: lexed.StorageText, ErrorCode: code, DurationMS: durationMS})
					if err == nil {
						terminal.Claim("completed")
					}
				} else if err == nil && w.manager.lifecycle != nil {
					_ = w.manager.lifecycle.Complete(deliveryCtx, messageIDs, firstMessageID, lexed.StorageText, durationMS)
				}
			}
		}
		if err != nil && markedDelivery {
			code := "companion_delivery_failed"
			if errors.Is(err, ErrWorkflowTrace) {
				code = "companion_delivery_trace_incomplete"
			} else if err.Error() == "companion_segment_delivery_unknown" {
				code = "companion_segment_delivery_unknown"
			} else if errors.Is(err, context.Canceled) {
				code = "companion_delivery_cancelled"
			}
			_ = companionLifecycle.FailCompanionDelivery(parent, messageIDs, batch.ID, code, "本批处理失败，请重新发送。", durationMS)
		} else if err != nil && w.manager.lifecycle != nil {
			_, _ = output.SendBatchText(parent, batch.Messages[0].Reply, "本批处理失败，请重新发送。")
			_ = w.manager.lifecycle.Fail(parent, messageIDs, "companion_batch_failed", durationMS)
		}
	} else if w.manager.lifecycle != nil {
		_ = w.manager.lifecycle.Fail(parent, messageIDs, "output_unavailable", 0)
	}
	timedOut := errors.Is(err, context.DeadlineExceeded)
	w.mu.Lock()
	w.processing = false
	if timedOut {
		w.state = Stopped
		pending := append([]Message(nil), w.queue...)
		w.queue = nil
		w.mu.Unlock()
		for _, message := range pending {
			if output != nil {
				_, _ = output.SendBatchText(parent, message.Reply, "上一批处理超时，未处理消息请重新发送。")
			}
			if w.manager.lifecycle != nil {
				_ = w.manager.lifecycle.Fail(parent, []string{message.ID}, "worker_timeout_stopped", 0)
			}
		}
		w.removeFromManager()
		return
	}
	w.state = Idle
	w.idleAt = time.Now()
	hasMore := len(w.queue) > 0
	w.mu.Unlock()
	if hasMore {
		w.signal()
	} else {
		w.scheduleIdleRecycle()
	}
}

func sendCompanionSegment(ctx context.Context, output Output, target ReplyTarget, text string) CompanionSendResult {
	if sender, ok := output.(CompanionSegmentSender); ok {
		return sender.SendCompanionSegment(ctx, target, text)
	}
	messageID, err := output.SendBatchText(ctx, target, text)
	if err == nil {
		return CompanionSendResult{MessageID: messageID, Outcome: CompanionSent}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return CompanionSendResult{Outcome: CompanionUnknown, Reason: "request_unknown"}
	}
	return CompanionSendResult{Outcome: CompanionUnknown, Reason: "send_unknown"}
}

func (w *channelWorker) recordCompanionSegment(ctx context.Context, batch Batch, processResult ProcessResult, index int, text string, result CompanionSendResult, retryCount int) error {
	digest := sha256.Sum256([]byte(text))
	event := CompanionWorkflowEvent{
		BatchID: batch.ID, SourceTraceIDs: traceIDs(batch.Messages), ThreadID: processResult.ThreadID, TurnID: processResult.TurnID,
		SegmentIndex: index, TextSHA256: fmt.Sprintf("%x", digest[:]), Result: result.Outcome, Reason: result.Reason,
		MessageID: result.MessageID, RetryCount: retryCount, At: time.Now(),
	}
	if writer := w.manager.cfg.WorkflowWriter; writer != nil {
		return writer.WriteCompanionSegment(ctx, event)
	}
	if logger := w.manager.cfg.WorkflowLogger; logger != nil {
		logger.Info("companion_segment_delivery",
			"event", "companion_segment_delivery", "batch_id", event.BatchID, "source_trace_ids", event.SourceTraceIDs,
			"thread_id", event.ThreadID, "turn_id", event.TurnID, "segment_index", event.SegmentIndex,
			"text_sha256", event.TextSHA256, "result", event.Result, "reason", event.Reason,
			"message_id", event.MessageID, "retry_count", event.RetryCount, "at", event.At.Format(time.RFC3339Nano),
		)
	}
	return nil
}

func traceIDs(messages []Message) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.TraceID)
	}
	return ids
}

func (w *channelWorker) removeFromManager() {
	w.manager.mu.Lock()
	if w.manager.workers[w.key.String()] == w {
		delete(w.manager.workers, w.key.String())
	}
	w.manager.mu.Unlock()
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
}

func (w *channelWorker) scheduleIdleRecycle() {
	time.AfterFunc(w.manager.cfg.IdleTimeout, func() {
		w.mu.Lock()
		eligible := w.state == Idle && !w.processing && len(w.queue) == 0 && time.Since(w.idleAt) >= w.manager.cfg.IdleTimeout
		if eligible {
			w.state = Stopped
		}
		w.mu.Unlock()
		if !eligible {
			return
		}
		w.removeFromManager()
	})
}
