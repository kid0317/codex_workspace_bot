package worker

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	presentation "github.com/kid0317/codex-workspace-bot/internal/output"
)

var (
	ErrQueueFull               = errors.New("worker queue full")
	ErrPoolSaturated           = errors.New("worker pool saturated")
	ErrClosed                  = errors.New("worker manager closed")
	ErrStopping                = errors.New("worker stopping")
	ErrControlInProgress       = errors.New("channel control in progress")
	ErrWorkflowTrace           = errors.New("companion_delivery_trace_incomplete")
	ErrCommandDeliveryRejected = errors.New("command delivery rejected")
	ErrCommandDeliveryUnknown  = errors.New("command delivery unknown")
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

// ActorPrincipal is ingress-derived, attempt-local authorization context. It
// is intentionally memory-only: it must not be inferred from model arguments
// or copied into logs, task definitions, or workflow traces.
type ActorPrincipal struct{ OpenID string }

type AppRuntime struct {
	ID, WorkspaceDir, WorkspaceMode, Model, Effort string
}

type Message struct {
	ID, TraceID, ChatGroupID string
	// ChatType and ChatID retain the persistent conversation identity. P2P
	// workers use sender OpenID as their queue key, so these cannot be derived
	// from Key when S08 builds its Langfuse session usage identity.
	ChatType, ChatID string
	Key              Key
	Runtime          AppRuntime
	Reply            ReplyTarget
	Actor            ActorPrincipal
	Query            string
	ReceivedAt       time.Time
	// HasRequiredAttachment makes this message an exclusive FIFO batch. The
	// owning worker must materialize its attachment before it can start a turn.
	HasRequiredAttachment bool
	AttachmentIDs         []string
	// AttachmentOutboxDir is assigned only by the attachment resolver for the
	// active attachment batch. It is never persisted or logged.
	AttachmentOutboxDir string
	// Goal marks an exclusive batch whose Query is the native Codex goal
	// objective. It is never merged with ordinary user input.
	Goal bool
	// ScheduledTaskRunID is assigned only by the trusted scheduler. It makes a
	// synthetic Prompt an exclusive FIFO item and prevents it from using the
	// normal message lifecycle/card ownership path.
	ScheduledTaskID     string
	ScheduledTaskRunID  string
	ScheduledClaimToken string
}

type Batch struct {
	ID                  string
	Key                 Key
	Runtime             AppRuntime
	Messages            []Message
	OnItem              func(PresentationItem) bool
	Goal                bool
	ScheduledTaskID     string
	ScheduledTaskRunID  string
	ScheduledClaimToken string
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
	// FinalText is populated only for a trusted ScheduledTaskRun while it is
	// being finalized. It is not persisted or logged by Worker; the S06
	// delivery outbox is its sole intended consumer.
	FinalText string
}
type Processor func(context.Context, Batch) (ProcessResult, error)

type Lifecycle interface {
	MarkProcessing(context.Context, []string) error
	Complete(context.Context, []string, string, string, int64) error
	Fail(context.Context, []string, string, int64) error
}

// ScheduledRunLifecycle owns only S06 run state. It intentionally has no
// message IDs or card IDs, so synthetic prompts cannot mutate normal ingress
// message state or S04 card ownership.
type ScheduledRunLifecycle interface {
	MarkScheduledRunning(context.Context, string, string) error
	CompleteScheduledRun(context.Context, string, string, bool, string, int64) error
}

// ScheduledRunResultLifecycle receives the ephemeral App Server final text
// for an S06 Prompt. It is deliberately separate from the normal Lifecycle:
// synthetic messages do not own message rows or S04 cards.
type ScheduledRunResultLifecycle interface {
	ScheduledRunLifecycle
	CompleteScheduledRunResult(context.Context, string, string, bool, string, int64, string, string, string) error
}

// ScheduledRunDiscardLifecycle owns queued scheduled runs removed by a channel
// control barrier. It deliberately has no delivery method: a /new or /cancel
// must not later publish a result card for work that never started.
type ScheduledRunDiscardLifecycle interface {
	DiscardScheduledRun(context.Context, string, string, string) error
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
	cfg          Config
	outputFor    OutputForBatch
	processor    Processor
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	workers      map[string]*channelWorker
	shuttingDown bool
	sequence     atomic.Uint64
	lifecycle    Lifecycle
	goalStop     GoalRunController
}

// GoalRunController is implemented by the Codex runtime. It owns the
// pause-confirmation and turn-drain protocol for a live Goal batch.
type GoalRunController interface {
	RequestGoalStop(context.Context, string) error
}

// Control is owned by a channel worker. Its Run function executes only after
// any active batch has released the channel, so thread mutations cannot race a
// turn or companion delivery from the same conversation.
type Control struct {
	Run     func(context.Context) error
	OnError func(context.Context, error)
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
	if m.ctx.Err() != nil || m.shuttingDown {
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

func (m *Manager) Close() {
	m.mu.Lock()
	m.shuttingDown = true
	workers := make([]*channelWorker, 0, len(m.workers))
	for _, w := range m.workers {
		workers = append(workers, w)
	}
	m.mu.Unlock()
	// Persist queued work before cancelling the manager context. In particular,
	// S06 runs are otherwise left queued until restart reconciliation merely
	// because the worker loop exits before it can observe its queue again.
	for _, w := range workers {
		w.discardQueuedForShutdown(context.Background())
	}
	m.cancel()
}

// Shutdown blocks new ingress, routes every active worker through the same
// control path as /cancel, and waits for each active batch to release before
// tearing down the shared runtime. Goal batches keep their existing
// pause-confirmation -> interrupt ordering through submitControl.
func (m *Manager) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("shutdown context is required")
	}
	m.mu.Lock()
	if m.ctx.Err() != nil {
		m.mu.Unlock()
		return nil
	}
	m.shuttingDown = true
	workers := make([]*channelWorker, 0, len(m.workers))
	for _, w := range m.workers {
		workers = append(workers, w)
	}
	m.mu.Unlock()
	if len(workers) == 0 {
		m.Close()
		return nil
	}

	errCh := make(chan error, len(workers))
	for _, w := range workers {
		w := w
		go func() {
			if err := w.submitControl(ctx, Control{Run: func(context.Context) error { return nil }}); err != nil && !errors.Is(err, ErrStopping) {
				errCh <- err
				return
			}
			if err := m.Cancel(ctx, w.key); err != nil {
				errCh <- err
				return
			}
			errCh <- nil
		}()
	}
	var errs []error
	for range workers {
		select {
		case err := <-errCh:
			if err != nil {
				errs = append(errs, err)
			}
		case <-ctx.Done():
			errs = append(errs, ctx.Err())
			m.Close()
			return errors.Join(errs...)
		}
	}
	m.Close()
	return errors.Join(errs...)
}

func (m *Manager) SetGoalRunController(controller GoalRunController) { m.goalStop = controller }

// SubmitControl establishes a channel barrier immediately, cancels the
// active batch, and schedules the supplied effect behind that batch. Normal
// ingress is rejected while the barrier is held instead of crossing a /new or
// /cancel boundary.
func (m *Manager) SubmitControl(ctx context.Context, key Key, control Control) error {
	if control.Run == nil {
		return errors.New("control run is required")
	}
	m.mu.Lock()
	if m.ctx.Err() != nil || m.shuttingDown {
		m.mu.Unlock()
		return ErrClosed
	}
	w := m.workers[key.String()]
	if w == nil {
		if len(m.workers) >= m.cfg.MaxWorkers {
			m.mu.Unlock()
			return ErrPoolSaturated
		}
		w = &channelWorker{manager: m, key: key, state: Idle, idleAt: time.Now(), wake: make(chan struct{}, 1), stop: make(chan struct{})}
		m.workers[key.String()] = w
		go w.run(m.ctx)
	}
	m.mu.Unlock()
	return w.submitControl(ctx, control)
}

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
	controls        []Control
	barrier         bool
	processing      bool
	state           State
	idleAt          time.Time
	activeCancel    context.CancelFunc
	activeDone      chan struct{}
	activeCancelled bool
	activeBatchID   string
	activeGoal      bool
	terminal        *TerminalArbiter
	delivery        *DeliverySlot
	done            chan struct{}
	stop            chan struct{}
	wake            chan struct{}
}

func (w *channelWorker) setActive(cancel context.CancelFunc) {
	w.mu.Lock()
	w.activeCancel = cancel
	cancelNow := w.activeCancelled
	w.mu.Unlock()
	if cancelNow {
		cancel()
	}
}

func (w *channelWorker) setGoalBatch(batch Batch) {
	w.mu.Lock()
	w.activeBatchID, w.activeGoal = batch.ID, batch.Goal
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
	w.activeBatchID, w.activeGoal = "", false
	w.activeCancelled = false
	w.terminal = nil
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
	if w.barrier {
		w.mu.Unlock()
		return ErrControlInProgress
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

func (w *channelWorker) submitControl(ctx context.Context, control Control) error {
	w.mu.Lock()
	if w.state == Stopping || w.state == Stopped {
		w.mu.Unlock()
		return ErrStopping
	}
	pending := append([]Message(nil), w.queue...)
	w.queue = nil
	w.barrier = true
	w.controls = append(w.controls, control)
	cancel := w.activeCancel
	batchID, goal := w.activeBatchID, w.activeGoal
	goalStop := w.manager.goalStop
	w.activeCancelled = true
	if w.terminal != nil {
		w.terminal.Claim("cancelled")
	}
	w.mu.Unlock()
	if goal && goalStop != nil {
		if err := goalStop.RequestGoalStop(ctx, batchID); err != nil {
			// Keep the barrier and Goal owner fail-closed. Falling back to a
			// context cancel would interrupt before pause confirmation and let a
			// later /new archive an active Goal.
			return fmt.Errorf("request goal pause: %w", err)
		}
		cancel = nil // GoalAttempt owns pause -> interrupt -> drain.
	}
	if cancel != nil {
		cancel()
	}
	if len(pending) > 0 && w.manager.lifecycle != nil {
		ids := make([]string, 0, len(pending))
		for _, message := range pending {
			if message.ScheduledTaskRunID == "" {
				ids = append(ids, message.ID)
				continue
			}
			discarder, ok := w.manager.lifecycle.(ScheduledRunDiscardLifecycle)
			if !ok {
				return fmt.Errorf("persist detached scheduled run: lifecycle is unavailable")
			}
			if err := discarder.DiscardScheduledRun(ctx, message.ScheduledTaskRunID, message.ScheduledClaimToken, "cancelled_by_channel_control"); err != nil {
				return fmt.Errorf("persist detached scheduled run: %w", err)
			}
		}
		if len(ids) > 0 {
			if err := w.manager.lifecycle.Fail(ctx, ids, "cancelled_by_command", 0); err != nil {
				return fmt.Errorf("persist detached queue: %w", err)
			}
		}
	}
	w.signal()
	return nil
}

func (w *channelWorker) discardQueuedForShutdown(ctx context.Context) {
	w.mu.Lock()
	pending := append([]Message(nil), w.queue...)
	w.queue = nil
	w.mu.Unlock()
	if len(pending) == 0 || w.manager.lifecycle == nil {
		return
	}
	ids := make([]string, 0, len(pending))
	for _, message := range pending {
		if message.ScheduledTaskRunID == "" {
			ids = append(ids, message.ID)
			continue
		}
		discarder, ok := w.manager.lifecycle.(ScheduledRunDiscardLifecycle)
		if !ok {
			slog.Error("scheduled_run_shutdown_discard_unavailable", "event", "scheduled_run_shutdown_discard_unavailable", "run_id", message.ScheduledTaskRunID)
			continue
		}
		if err := discarder.DiscardScheduledRun(ctx, message.ScheduledTaskRunID, message.ScheduledClaimToken, "cancelled_by_channel_control"); err != nil {
			slog.Error("scheduled_run_shutdown_discard_failed", "event", "scheduled_run_shutdown_discard_failed", "run_id", message.ScheduledTaskRunID, "error", err)
		}
	}
	if len(ids) > 0 {
		if err := w.manager.lifecycle.Fail(ctx, ids, "worker_shutdown_stopped", 0); err != nil {
			slog.Error("worker_shutdown_queue_persist_failed", "event", "worker_shutdown_queue_persist_failed", "channel_key", w.key.String(), "error", err)
		}
	}
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
			control, hasControl := w.nextControl()
			if hasControl {
				w.processControl(ctx, control)
				continue
			}
			batch, ok := w.nextBatch()
			if !ok {
				break
			}
			w.process(ctx, batch)
		}
	}
}

func (w *channelWorker) nextControl() (Control, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.processing || len(w.controls) == 0 {
		return Control{}, false
	}
	control := w.controls[0]
	w.controls = w.controls[1:]
	return control, true
}

func (w *channelWorker) processControl(parent context.Context, control Control) {
	ctx, cancel := context.WithTimeout(parent, w.manager.cfg.StopGrace)
	defer cancel()
	w.mu.Lock()
	done := w.activeDone
	delivery := w.delivery
	w.mu.Unlock()
	if delivery != nil {
		_ = delivery.CancelAndWait(ctx)
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
	if err := control.Run(context.Background()); err != nil && control.OnError != nil {
		control.OnError(context.Background(), err)
	}
	w.mu.Lock()
	if len(w.controls) == 0 {
		w.barrier = false
	}
	w.mu.Unlock()
	w.signal()
}

func (w *channelWorker) nextBatch() (Batch, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.processing || len(w.queue) == 0 {
		return Batch{}, false
	}
	batchSize := len(w.queue)
	for index, message := range w.queue {
		if index > 0 && message.Actor.OpenID != w.queue[0].Actor.OpenID {
			batchSize = index
			break
		}
		if message.Goal {
			batchSize = index
			if index == 0 {
				batchSize = 1
			}
			break
		}
		if message.ScheduledTaskRunID != "" {
			batchSize = index
			if index == 0 {
				batchSize = 1
			}
			break
		}
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
	w.activeCancel = nil
	w.activeDone = make(chan struct{})
	w.activeCancelled = false
	w.terminal = NewTerminalArbiter()
	id := uuid.NewString()
	return Batch{ID: id, Key: w.key, Runtime: messages[0].Runtime, Messages: messages, Goal: messages[0].Goal, ScheduledTaskID: messages[0].ScheduledTaskID, ScheduledTaskRunID: messages[0].ScheduledTaskRunID, ScheduledClaimToken: messages[0].ScheduledClaimToken}, true
}

func (w *channelWorker) process(parent context.Context, batch Batch) {
	if batch.ScheduledTaskRunID != "" {
		w.processScheduled(parent, batch)
		return
	}
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
			w.setGoalBatch(batch)
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
				// Keep channel ownership until the processor has observed the
				// cancellation. A delayed turn/start may still bind a turn ID and
				// must be interrupted before /new can archive the Thread.
				completed := <-result
				processResult, err = completed.result, completed.err
			}
			w.mu.Lock()
			terminalReason := ""
			if w.terminal != nil {
				terminalReason = w.terminal.Reason()
			}
			w.mu.Unlock()
			if terminalReason == "cancelled" {
				err = context.Canceled
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
			if batch.Goal {
				content = "目标已完成，但未收到可展示的最终摘要；请检查已完成的操作与日志。"
			}
			if projection != nil && projection.Final() != "" {
				content = projection.Final()
			}
			progress := ""
			if projection != nil {
				progress = projection.Progress()
			}
			if batch.Goal && progress == "" && err == nil {
				progress = "目标已完成；本轮未收到可展示的阶段进展。"
			}
			if errors.Is(err, context.DeadlineExceeded) {
				timeoutContent := "本批处理超时，请重新发送。"
				var updateErr error
				if zoneOutput, ok := output.(DualZoneOutput); ok && batch.Goal {
					updateErr = zoneOutput.UpdateBatchCardZones(parent, cardID, timeoutContent, goalTerminalProgress(err), "目标未完成", true)
				} else {
					updateErr = output.UpdateBatchCard(parent, cardID, timeoutContent)
				}
				if updateErr != nil {
					_, _ = output.SendBatchText(parent, batch.Messages[0].Reply, "本批处理超时，请重新发送。")
				}
				if w.manager.lifecycle != nil {
					_ = w.manager.lifecycle.Fail(parent, messageIDs, "worker_timeout_stopped", durationMS)
				}
			} else if err != nil {
				slog.Error("batch_processor_failed", "event", "batch_processor_failed", "batch_id", batch.ID, "channel_key", batch.Key.String(), "error", err)
				failure := failureMessage(batch, err)
				var updateErr error
				if zoneOutput, ok := output.(DualZoneOutput); ok && batch.Goal {
					updateErr = zoneOutput.UpdateBatchCardZones(parent, cardID, failure, goalTerminalProgress(err), "目标未完成", true)
				} else {
					updateErr = output.UpdateBatchCard(parent, cardID, failure)
				}
				if updateErr != nil {
					_, _ = output.SendBatchText(parent, batch.Messages[0].Reply, failure)
				}
				if w.manager.lifecycle != nil {
					_ = w.manager.lifecycle.Fail(parent, messageIDs, terminalErrorCode(err), durationMS)
				}
			} else {
				var updateErr error
				if zoneOutput, ok := output.(DualZoneOutput); ok && projection != nil {
					summary := "已完成"
					if batch.Goal {
						summary = "目标已完成"
					}
					updateErr = zoneOutput.UpdateBatchCardZones(parent, cardID, content, progress, summary, true)
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

func (w *channelWorker) processScheduled(parent context.Context, batch Batch) {
	lifecycle, ok := w.manager.lifecycle.(ScheduledRunLifecycle)
	if !ok || batch.ScheduledClaimToken == "" {
		w.finishScheduled(parent, batch, false, "failed_lifecycle", 0, "", "", "", false)
		return
	}
	if err := lifecycle.MarkScheduledRunning(parent, batch.ScheduledTaskRunID, batch.ScheduledClaimToken); err != nil {
		w.finishScheduled(parent, batch, false, "failed_run_claim", 0, "", "", "", false)
		return
	}
	ctx, cancel := context.WithTimeout(parent, w.manager.cfg.ProcessTimeout)
	w.setActive(cancel)
	defer w.clearActive()
	started := time.Now()
	result, err := w.manager.processor(ctx, batch)
	cancel()
	if err == nil && w.terminal != nil && w.terminal.Reason() == "cancelled" {
		err = context.Canceled
	}
	code := ""
	if err != nil {
		code = terminalErrorCode(err)
		if errors.Is(err, context.Canceled) && w.terminal != nil && w.terminal.Reason() == "cancelled" {
			code = "cancelled_by_channel_control"
		}
	}
	duration := result.DurationMS
	if duration == 0 {
		duration = time.Since(started).Milliseconds()
	}
	w.finishScheduled(parent, batch, err == nil, code, duration, result.FinalText, result.ThreadID, result.TurnID, errors.Is(err, context.DeadlineExceeded))
}

func (w *channelWorker) finishScheduled(parent context.Context, batch Batch, succeeded bool, code string, duration int64, finalText, threadID, turnID string, stopAfterTimeout bool) {
	if code == "cancelled_by_channel_control" {
		if lifecycle, ok := w.manager.lifecycle.(ScheduledRunDiscardLifecycle); ok && batch.ScheduledClaimToken != "" {
			_ = lifecycle.DiscardScheduledRun(parent, batch.ScheduledTaskRunID, batch.ScheduledClaimToken, code)
			if stopAfterTimeout {
				w.stopAfterScheduledTimeout(parent)
				return
			}
			w.finishScheduledWorkerState()
			return
		}
	}
	if lifecycle, ok := w.manager.lifecycle.(ScheduledRunResultLifecycle); ok && batch.ScheduledClaimToken != "" {
		_ = lifecycle.CompleteScheduledRunResult(parent, batch.ScheduledTaskRunID, batch.ScheduledClaimToken, succeeded, code, duration, finalText, threadID, turnID)
	} else if lifecycle, ok := w.manager.lifecycle.(ScheduledRunLifecycle); ok && batch.ScheduledClaimToken != "" {
		_ = lifecycle.CompleteScheduledRun(parent, batch.ScheduledTaskRunID, batch.ScheduledClaimToken, succeeded, code, duration)
	}
	if stopAfterTimeout {
		w.stopAfterScheduledTimeout(parent)
		return
	}
	w.finishScheduledWorkerState()
}

// stopAfterScheduledTimeout prevents a timed-out scheduled turn from allowing
// a later queued turn in the same channel to cross its terminal boundary.
func (w *channelWorker) stopAfterScheduledTimeout(ctx context.Context) {
	w.mu.Lock()
	w.processing = false
	w.state = Stopped
	pending := append([]Message(nil), w.queue...)
	w.queue = nil
	w.mu.Unlock()
	if w.manager.lifecycle != nil {
		ordinaryIDs := make([]string, 0, len(pending))
		for _, message := range pending {
			if message.ScheduledTaskRunID == "" {
				ordinaryIDs = append(ordinaryIDs, message.ID)
				continue
			}
			discarder, ok := w.manager.lifecycle.(ScheduledRunDiscardLifecycle)
			if !ok {
				slog.Error("scheduled_run_timeout_discard_unavailable", "event", "scheduled_run_timeout_discard_unavailable", "run_id", message.ScheduledTaskRunID)
				continue
			}
			if err := discarder.DiscardScheduledRun(ctx, message.ScheduledTaskRunID, message.ScheduledClaimToken, "cancelled_by_channel_control"); err != nil {
				slog.Error("scheduled_run_timeout_discard_failed", "event", "scheduled_run_timeout_discard_failed", "run_id", message.ScheduledTaskRunID, "error", err)
			}
		}
		if len(ordinaryIDs) > 0 {
			if err := w.manager.lifecycle.Fail(ctx, ordinaryIDs, "worker_timeout_stopped", 0); err != nil {
				slog.Error("scheduled_run_timeout_queue_persist_failed", "event", "scheduled_run_timeout_queue_persist_failed", "channel_key", w.key.String(), "error", err)
			}
		}
	}
	w.removeFromManager()
}

func (w *channelWorker) finishScheduledWorkerState() {
	w.mu.Lock()
	w.processing = false
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

func goalTerminalProgress(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "目标执行超时，未完成。"
	case errors.Is(err, context.Canceled):
		return "目标已中断。"
	case strings.Contains(err.Error(), `goal terminal status "paused"`):
		return "目标已暂停。"
	case strings.Contains(err.Error(), `goal terminal status "budget_limited"`):
		return "目标达到预算限制，未宣告完成。"
	default:
		return "目标执行未完成。"
	}
}

func terminalErrorCode(err error) string {
	if err != nil && err.Error() == "presentation_backpressure" {
		return "presentation_backpressure"
	}
	return "worker_process_failed"
}

func failureMessage(batch Batch, err error) string {
	if !batch.Goal || err == nil {
		return "本批处理失败，请重新发送。"
	}
	switch {
	case strings.Contains(err.Error(), `goal terminal status "paused"`):
		return "目标已暂停。"
	case strings.Contains(err.Error(), `goal terminal status "budget_limited"`):
		return "目标达到预算限制，未宣告完成。"
	default:
		return "目标执行未完成，请稍后重新发起。"
	}
}

func (w *channelWorker) processCompanion(parent context.Context, batch Batch, messageIDs []string, output Output, err error) {
	w.mu.Lock()
	terminal := w.terminal
	if terminal == nil {
		terminal = NewTerminalArbiter()
		w.terminal = terminal
	}
	w.mu.Unlock()
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
		w.setGoalBatch(batch)
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
			completed := <-result
			processResult, err = completed.result, completed.err
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
