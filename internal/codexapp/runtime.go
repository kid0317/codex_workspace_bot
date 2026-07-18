package codexapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/kid0317/codex-workspace-bot/internal/observability"
)

var (
	ErrUnavailable = errors.New("app server unavailable")
	ErrExited      = errors.New("app server exited")
)

type Availability string

const (
	Ready       Availability = "ready"
	Unavailable Availability = "unavailable"
)

type Config struct {
	Command                                     string
	RPCTimeout, TurnTimeout, IdleTimeout, Grace time.Duration
	Debug                                       bool
	DebugDir                                    string
	Now                                         func() time.Time
}

type starter func() (io.ReadWriteCloser, error)

type Runtime struct {
	cfg              Config
	start            starter
	mu               sync.Mutex
	availability     Availability
	closed           bool
	current          *generation
	nextGeneration   uint64
	attempts         map[*attempt]struct{}
	goals            map[string]*GoalAttempt
	pendingStops     map[string]struct{}
	earlyGoalUpdates map[string]Event
	timeline         *Timeline
}

type generation struct {
	id     uint64
	client *Client
}
type attempt struct {
	mu                   sync.Mutex
	generation           uint64
	threadID, turnID     string
	pending              []Event
	pendingBytes         int
	presentationOver     bool
	done                 chan error
	finished             bool
	stopping             bool
	progress             chan struct{}
	route                RouteMetadata
	onItem               func(CompletedItem) bool
	onTurnCompleted      func(TurnCompleted)
	observation          *observability.Attempt
	toolHandler          ToolHandler
	ctx                  context.Context
	cancel               context.CancelFunc
	actionSlots          chan struct{}
	bound                chan struct{}
	boundOnce            sync.Once
	goal                 bool
	knownTurns           map[string]struct{}
	goalTerminal         bool
	goalErr              error
	awaitingContinuation bool
	turnChanged          chan struct{}
	toolCalls            map[string]*toolCallState
	lastTokenUsage       *observability.Usage
	pauseConfirmed       bool
	stopRequested        bool
}
type toolCallState struct {
	result                   any
	handlerErr               error
	handlerStarted, terminal bool
}

// GoalAttempt reserves a thread-level owner before thread/goal/set. It keeps
// notifications received in the set→turn/start window from becoming unrouted.
type GoalAttempt struct {
	runtime *Runtime
	gen     *generation
	attempt *attempt
	params  TurnStartParams
}

const (
	maxPresentationEvents = 64
	maxPresentationBytes  = 256 * 1024
)

type RouteMetadata struct {
	AppID, ChannelKey, ChatGroupID, AttemptID string
}

func NewRuntime(cfg Config) *Runtime {
	return NewRuntimeWithStarter(cfg, func() (io.ReadWriteCloser, error) { return startProcess(cfg.Command) })
}
func NewRuntimeWithStarter(cfg Config, start starter) *Runtime {
	if cfg.Command == "" {
		cfg.Command = "codex"
	}
	if cfg.RPCTimeout == 0 {
		cfg.RPCTimeout = 30 * time.Second
	}
	if cfg.TurnTimeout == 0 {
		cfg.TurnTimeout = 500 * time.Second
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 90 * time.Second
	}
	if cfg.Grace == 0 {
		cfg.Grace = 10 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Runtime{cfg: cfg, start: start, availability: Unavailable, attempts: make(map[*attempt]struct{}), goals: make(map[string]*GoalAttempt), pendingStops: make(map[string]struct{}), earlyGoalUpdates: make(map[string]Event)}
}

func (r *Runtime) Start(ctx context.Context) error {
	if r.cfg.Debug {
		timeline, err := NewTimeline(r.cfg.DebugDir, fmt.Sprintf("%d", time.Now().UnixNano()), r.snapshot)
		if err != nil {
			return err
		}
		r.mu.Lock()
		r.timeline = timeline
		r.mu.Unlock()
	}
	if err := r.startGeneration(ctx); err != nil {
		r.closeTimeline()
		return err
	}
	return nil
}
func (r *Runtime) Availability() Availability {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.availability
}
func (r *Runtime) IsReady() bool { return r.Availability() == Ready }
func (r *Runtime) Close() {
	r.mu.Lock()
	r.closed = true
	current := r.current
	r.current = nil
	r.availability = Unavailable
	r.mu.Unlock()
	if current != nil {
		current.client.Close()
		r.closeTimeline()
	}
	r.failAttempts(0, ErrExited)
}

func (r *Runtime) startGeneration(parent context.Context) error {
	conn, err := r.start()
	if err != nil {
		return fmt.Errorf("start owned app server: %w", err)
	}
	r.mu.Lock()
	r.nextGeneration++
	id := r.nextGeneration
	r.mu.Unlock()
	gen := &generation{id: id}
	var observer Observer
	r.mu.Lock()
	timeline := r.timeline
	r.mu.Unlock()
	if timeline != nil {
		observer = timeline
	}
	gen.client = NewClient(conn, observer, r.dispatch, r.handleServerRequest)
	gen.client.SetServerResponseObserver(r.handleServerResponse)
	ctx, cancel := context.WithTimeout(parent, r.cfg.RPCTimeout)
	defer cancel()
	if _, err := gen.client.Call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "codex-workspace-bot", "version": "s03"}, "capabilities": map[string]any{"experimentalApi": true}}); err != nil {
		gen.client.Close()
		return fmt.Errorf("initialize owned app server: %w", err)
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		gen.client.Close()
		return ErrClientClosed
	}
	r.current = gen
	r.availability = Ready
	r.mu.Unlock()
	go func() { <-gen.client.Done(); r.onGenerationExit(gen) }()
	return nil
}

func (r *Runtime) onGenerationExit(gen *generation) {
	r.mu.Lock()
	if r.closed || r.current != gen {
		r.mu.Unlock()
		return
	}
	r.current = nil
	r.availability = Unavailable
	r.mu.Unlock()
	r.failAttempts(gen.id, ErrExited)
	// One exit gets one replacement. A successful initialize ends this recovery
	// chain; a failed replacement remains unavailable until the bot is restarted.
	if err := r.startGeneration(context.Background()); err != nil {
		return
	}
}

func (r *Runtime) closeTimeline() {
	r.mu.Lock()
	timeline := r.timeline
	r.timeline = nil
	r.mu.Unlock()
	if timeline != nil {
		_ = timeline.Close()
	}
}

func (r *Runtime) client() (*generation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.availability != Ready || r.current == nil {
		return nil, ErrUnavailable
	}
	return r.current, nil
}
func (r *Runtime) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	gen, err := r.client()
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, r.cfg.RPCTimeout)
	defer cancel()
	return gen.client.Call(requestCtx, method, params)
}

func (r *Runtime) RegisterGoalThread(threadID string) {
	r.mu.Lock()
	timeline := r.timeline
	r.mu.Unlock()
	if timeline != nil {
		timeline.RegisterGoalThread(threadID)
	}
}

func (r *Runtime) UnregisterGoalThread(threadID string) {
	r.mu.Lock()
	timeline := r.timeline
	r.mu.Unlock()
	if timeline != nil {
		timeline.UnregisterGoalThread(threadID)
	}
}

func (r *Runtime) StartTurn(ctx context.Context, threadID string, params TurnStartParams) (time.Time, error) {
	return r.startAttempt(ctx, threadID, params, false)
}

// StartGoal starts the objective turn for an already-active thread Goal. Unlike
// StartTurn it remains owned across App Server continuation turns and finishes
// only when the Goal reports a terminal status (or a local failure occurs).
func (r *Runtime) StartGoal(ctx context.Context, threadID string, params TurnStartParams) (time.Time, error) {
	goal, err := r.PrepareGoal(ctx, threadID, params)
	if err != nil {
		return time.Time{}, err
	}
	defer goal.Close()
	return goal.Start(ctx)
}

func (r *Runtime) PrepareGoal(ctx context.Context, threadID string, params TurnStartParams) (*GoalAttempt, error) {
	gen, err := r.client()
	if err != nil {
		return nil, err
	}
	attemptCtx, attemptCancel := context.WithCancel(ctx)
	a := &attempt{generation: gen.id, threadID: threadID, done: make(chan error, 1), progress: make(chan struct{}, 1), route: params.Route, onItem: params.OnItem, onTurnCompleted: params.OnTurnCompleted, observation: params.Observation, toolHandler: params.ToolHandler, ctx: attemptCtx, cancel: attemptCancel, actionSlots: make(chan struct{}, 4), bound: make(chan struct{}), goal: true, knownTurns: make(map[string]struct{}), turnChanged: make(chan struct{}, 1)}
	r.mu.Lock()
	r.attempts[a] = struct{}{}
	early := r.earlyGoalUpdates[threadID]
	delete(r.earlyGoalUpdates, threadID)
	r.mu.Unlock()
	if early.Method != "" {
		r.routeAttempt(a, early)
	}
	goal := &GoalAttempt{runtime: r, gen: gen, attempt: a, params: params}
	if params.Route.AttemptID != "" {
		r.mu.Lock()
		r.goals[params.Route.AttemptID] = goal
		if _, stopping := r.pendingStops[params.Route.AttemptID]; stopping {
			goal.attempt.stopRequested = true
			delete(r.pendingStops, params.Route.AttemptID)
		}
		r.mu.Unlock()
	}
	return goal, nil
}

func (g *GoalAttempt) Done() <-chan error { return g.attempt.done }

func (g *GoalAttempt) Close() {
	if g == nil || g.runtime == nil || g.attempt == nil {
		return
	}
	g.attempt.cancel()
	g.runtime.mu.Lock()
	delete(g.runtime.attempts, g.attempt)
	if g.params.Route.AttemptID != "" && g.runtime.goals[g.params.Route.AttemptID] == g {
		delete(g.runtime.goals, g.params.Route.AttemptID)
	}
	g.runtime.mu.Unlock()
	g.runtime.UnregisterGoalThread(g.attempt.threadID)
}

// RequestGoalStop synchronously pauses a registered Goal before its worker
// interrupts the active Turn. A missing goal means the batch has not reached
// registration yet and is treated as unavailable to the caller.
func (r *Runtime) RequestGoalStop(ctx context.Context, attemptID string) error {
	r.mu.Lock()
	goal := r.goals[attemptID]
	if goal == nil {
		r.pendingStops[attemptID] = struct{}{}
	}
	r.mu.Unlock()
	if goal == nil {
		return nil
	}
	return goal.BeginStop(ctx)
}

func (g *GoalAttempt) BeginStop(ctx context.Context) error {
	a := g.attempt
	a.mu.Lock()
	if a.pauseConfirmed || a.goalTerminal {
		a.mu.Unlock()
		return nil
	}
	a.stopping = true
	turnID := a.turnID
	a.mu.Unlock()
	if _, err := g.runtime.Call(ctx, "thread/goal/set", map[string]any{"threadId": a.threadID, "status": "paused"}); err != nil {
		g.gen.client.Close()
		return fmt.Errorf("pause goal: %w", err)
	}
	result, err := g.runtime.Call(ctx, "thread/goal/get", map[string]any{"threadId": a.threadID})
	if err != nil {
		g.gen.client.Close()
		return fmt.Errorf("confirm paused goal: %w", err)
	}
	var parsed struct {
		Goal struct {
			Status string `json:"status"`
		} `json:"goal"`
	}
	if json.Unmarshal(result, &parsed) != nil {
		g.gen.client.Close()
		return errors.New("goal pause was not confirmed")
	}
	a.mu.Lock()
	switch parsed.Goal.Status {
	case "paused":
		a.goalTerminal, a.pauseConfirmed, a.goalErr = true, true, fmt.Errorf(`goal terminal status "paused"`)
	case "complete", "completed":
		a.goalTerminal, a.goalErr = true, nil
	case "budget_limited":
		a.goalTerminal, a.goalErr = true, fmt.Errorf(`goal terminal status "budget_limited"`)
	default:
		a.mu.Unlock()
		g.gen.client.Close()
		return errors.New("goal pause was not confirmed")
	}
	goalErr := a.goalErr
	a.mu.Unlock()
	if parsed.Goal.Status == "complete" || parsed.Goal.Status == "completed" || parsed.Goal.Status == "budget_limited" {
		// This is already a server-authoritative terminal. Keep its current
		// turn in terminal drain rather than interrupting or replacing it.
		if turnID == "" {
			g.runtime.finish(a, goalErr)
		}
		return nil
	}
	if turnID == "" {
		g.runtime.finish(a, goalErr)
		return nil
	}
	interruptCtx, cancel := context.WithTimeout(context.Background(), g.runtime.cfg.Grace)
	defer cancel()
	_, _ = g.gen.client.Call(interruptCtx, "turn/interrupt", map[string]any{"threadId": a.threadID, "turnId": turnID})
	return nil
}

func (g *GoalAttempt) Start(ctx context.Context) (time.Time, error) {
	a := g.attempt
	a.mu.Lock()
	stopRequested := a.stopRequested
	if a.finished {
		a.mu.Unlock()
		select {
		case err := <-a.done:
			return time.Time{}, err
		default:
			return time.Time{}, errors.New("goal finished before first turn")
		}
	}
	a.mu.Unlock()
	if stopRequested {
		if err := g.BeginStop(ctx); err != nil {
			return time.Time{}, err
		}
		select {
		case err := <-a.done:
			return time.Time{}, err
		default:
			return time.Time{}, errors.New("goal was stopped before first turn")
		}
	}
	params := g.params
	params.ThreadID = a.threadID
	responseCtx, cancel := context.WithTimeout(context.Background(), g.runtime.cfg.RPCTimeout)
	result, err := g.gen.client.Call(responseCtx, "turn/start", params)
	cancel()
	if err != nil {
		g.gen.client.Close()
		return time.Time{}, err
	}
	startedAt := g.runtime.cfg.Now()
	var parsed struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil || parsed.Turn.ID == "" {
		g.gen.client.Close()
		return startedAt, fmt.Errorf("turn/start missing turn id")
	}
	if params.OnTurnStarted != nil {
		params.OnTurnStarted(parsed.Turn.ID)
	}
	g.runtime.bindAttempt(a, parsed.Turn.ID)
	a.mu.Lock()
	finished := a.finished
	a.mu.Unlock()
	if finished {
		go func() {
			interruptCtx, interruptCancel := context.WithTimeout(context.Background(), g.runtime.cfg.Grace)
			defer interruptCancel()
			_, _ = g.gen.client.Call(interruptCtx, "turn/interrupt", map[string]any{"threadId": a.threadID, "turnId": parsed.Turn.ID})
		}()
		select {
		case err := <-a.done:
			return startedAt, err
		default:
			return startedAt, errors.New("goal finished before first turn response")
		}
	}
	return g.runtime.waitAttempt(ctx, g.gen, a, startedAt)
}

func (r *Runtime) startAttempt(ctx context.Context, threadID string, params TurnStartParams, goal bool) (time.Time, error) {
	gen, err := r.client()
	if err != nil {
		return time.Time{}, err
	}
	attemptCtx, attemptCancel := context.WithCancel(ctx)
	a := &attempt{generation: gen.id, threadID: threadID, done: make(chan error, 1), progress: make(chan struct{}, 1), route: params.Route, onItem: params.OnItem, onTurnCompleted: params.OnTurnCompleted, observation: params.Observation, toolHandler: params.ToolHandler, ctx: attemptCtx, cancel: attemptCancel, actionSlots: make(chan struct{}, 4), bound: make(chan struct{}), goal: goal, knownTurns: make(map[string]struct{}), turnChanged: make(chan struct{}, 1)}
	r.mu.Lock()
	r.attempts[a] = struct{}{}
	r.mu.Unlock()
	defer func() {
		attemptCancel()
		r.mu.Lock()
		delete(r.attempts, a)
		r.mu.Unlock()
	}()
	params.ThreadID = threadID
	// The request lifetime is intentionally independent from the channel's
	// cancellation. App Server may accept turn/start before replying; retaining
	// correlation lets us bind a late turn ID and interrupt that accepted turn.
	responseCtx, cancel := context.WithTimeout(context.Background(), r.cfg.RPCTimeout)
	result, err := gen.client.Call(responseCtx, "turn/start", params)
	cancel()
	if err != nil {
		gen.client.Close()
		return time.Time{}, err
	}
	startedAt := r.cfg.Now()
	var parsed struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil || parsed.Turn.ID == "" {
		gen.client.Close()
		return startedAt, fmt.Errorf("turn/start missing turn id")
	}
	if params.OnTurnStarted != nil {
		params.OnTurnStarted(parsed.Turn.ID)
	}
	r.bindAttempt(a, parsed.Turn.ID)
	if ctx.Err() != nil {
		return startedAt, r.interruptAndFail(gen, a, ctx.Err().Error())
	}
	return r.waitAttempt(ctx, gen, a, startedAt)
}

func (r *Runtime) waitAttempt(ctx context.Context, gen *generation, a *attempt, startedAt time.Time) (time.Time, error) {
	total := time.NewTimer(r.cfg.TurnTimeout)
	defer total.Stop()
	idle := time.NewTimer(r.cfg.IdleTimeout)
	defer idle.Stop()
	for {
		select {
		case err := <-a.done:
			return startedAt, err
		case <-a.progress:
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(r.cfg.IdleTimeout)
		case <-a.turnChanged:
			resetTimer(total, r.cfg.TurnTimeout)
			resetTimer(idle, r.cfg.IdleTimeout)
		case <-total.C:
			return startedAt, r.interruptAndFail(gen, a, "turn total timeout")
		case <-idle.C:
			return startedAt, r.interruptAndFail(gen, a, "turn idle timeout")
		case <-ctx.Done():
			return startedAt, r.interruptAndFail(gen, a, ctx.Err().Error())
		}
	}
}

func (r *Runtime) handleServerRequest(event Event) (any, error) {
	if event.Method != "item/tool/call" {
		return nil, fmt.Errorf("unsupported server request: %s", event.Method)
	}
	var call ToolCall
	if err := json.Unmarshal(event.Params, &call); err != nil || call.ThreadID == "" || call.TurnID == "" || call.CallID == "" || call.Tool == "" {
		return ToolResult{Success: false, ContentItems: []ToolContentItem{{Type: "inputText", Text: "tool request is invalid"}}}, nil
	}
	r.mu.Lock()
	attempts := make([]*attempt, 0, len(r.attempts))
	for a := range r.attempts {
		attempts = append(attempts, a)
	}
	r.mu.Unlock()
	for _, a := range attempts {
		a.mu.Lock()
		sameThread := a.threadID == call.ThreadID
		bound := a.turnID != ""
		matches := sameThread && bound && a.turnID == call.TurnID && !a.finished
		wait := a.boundLocked()
		handler := a.toolHandler
		observation := a.observation
		handlerCtx := a.ctx
		a.mu.Unlock()
		if sameThread && !bound {
			<-wait
			a.mu.Lock()
			matches = a.turnID == call.TurnID && !a.finished
			handler = a.toolHandler
			observation = a.observation
			handlerCtx = a.ctx
			a.mu.Unlock()
		}
		if !matches {
			continue
		}
		if handler == nil {
			return ToolResult{Success: false, ContentItems: []ToolContentItem{{Type: "inputText", Text: "tool is unavailable for this turn"}}}, nil
		}
		// Register the request under the same attempt mutex used by finish. This
		// is the terminal fence: once finish wins, no later handler may acquire a
		// slot and cause a side effect.
		a.mu.Lock()
		if a.finished || a.turnID != call.TurnID {
			a.mu.Unlock()
			return ToolResult{Success: false, ContentItems: []ToolContentItem{{Type: "inputText", Text: "tool request arrived after turn terminal"}}}, nil
		}
		if a.toolCalls == nil {
			a.toolCalls = make(map[string]*toolCallState)
		}
		if _, exists := a.toolCalls[call.CallID]; !exists {
			a.toolCalls[call.CallID] = &toolCallState{}
		}
		generation := a.generation
		a.mu.Unlock()
		if observation != nil {
			observation.RecordProtocolEvent(generation, event.Seq, event.Method, event.Params)
			if !observation.StartTool(call.CallID, call.Tool, call.Arguments) {
				return ToolResult{Success: false, ContentItems: []ToolContentItem{{Type: "inputText", Text: "tool request arrived after protocol terminal"}}}, nil
			}
		}
		if handlerCtx == nil {
			handlerCtx = context.Background()
		}
		select {
		case a.actionSlots <- struct{}{}:
			defer func() { <-a.actionSlots }()
		case <-handlerCtx.Done():
			return ToolResult{Success: false, ContentItems: []ToolContentItem{{Type: "inputText", Text: "tool call was cancelled"}}}, nil
		}
		a.mu.Lock()
		state := a.toolCalls[call.CallID]
		if a.finished || a.turnID != call.TurnID || state == nil || state.terminal || state.handlerStarted {
			a.mu.Unlock()
			return ToolResult{Success: false, ContentItems: []ToolContentItem{{Type: "inputText", Text: "tool call was cancelled before execution"}}}, nil
		}
		// Same-mutex handler-start permit: finish marks unconsumed permits as
		// terminal. If terminal wins, no handler side effect can start.
		state.handlerStarted = true
		a.mu.Unlock()
		result, err := handler(handlerCtx, call)
		a.mu.Lock()
		if state := a.toolCalls[call.CallID]; state != nil {
			state.result, state.handlerErr = result, err
		}
		a.mu.Unlock()
		return result, err
	}
	return ToolResult{Success: false, ContentItems: []ToolContentItem{{Type: "inputText", Text: "tool request does not match the active turn"}}}, nil
}

func (r *Runtime) handleServerResponse(event Event, writeErr error) {
	if event.Method != "item/tool/call" {
		return
	}
	var call ToolCall
	if json.Unmarshal(event.Params, &call) != nil || call.CallID == "" {
		return
	}
	r.mu.Lock()
	attempts := make([]*attempt, 0, len(r.attempts))
	for a := range r.attempts {
		attempts = append(attempts, a)
	}
	r.mu.Unlock()
	for _, a := range attempts {
		a.mu.Lock()
		if a.threadID != call.ThreadID || a.turnID != call.TurnID {
			a.mu.Unlock()
			continue
		}
		state := a.toolCalls[call.CallID]
		observation := a.observation
		if state != nil {
			delete(a.toolCalls, call.CallID)
		}
		a.mu.Unlock()
		if state == nil || observation == nil {
			return
		}
		if writeErr != nil {
			observation.EndTool(call.CallID, map[string]any{"handler_result": state.result, "response_write": "failed"}, writeErr)
		} else if state.handlerErr != nil {
			observation.EndTool(call.CallID, map[string]any{"handler_result": state.result, "response_write": "error_response_written"}, state.handlerErr)
		} else {
			observation.EndTool(call.CallID, map[string]any{"handler_result": state.result, "response_write": "written"}, nil)
		}
		return
	}
}

func (r *Runtime) bindAttempt(a *attempt, turnID string) bool {
	a.mu.Lock()
	bound := a.boundLocked()
	if a.finished || (a.goal && (a.goalTerminal || a.stopping)) {
		a.mu.Unlock()
		return false
	}
	if a.goal && a.turnID != "" && a.turnID != turnID {
		if _, alreadyKnown := a.knownTurns[turnID]; alreadyKnown {
			a.mu.Unlock()
			return false
		}
	}
	if a.turnID != turnID {
		// tokenUsage.total is cumulative for a Thread. A fallback belongs only
		// to the turn that observed it; never carry a prior turn's last snapshot
		// into a newly bound continuation.
		a.lastTokenUsage = nil
	}
	a.turnID = turnID
	if a.goal {
		a.knownTurns[turnID] = struct{}{}
		a.awaitingContinuation = false
	}
	pending := a.pending
	presentationOver := a.presentationOver
	a.pending = nil
	a.mu.Unlock()
	a.boundOnce.Do(func() { close(bound) })
	if presentationOver {
		r.interruptAttempt(a, "presentation_backpressure")
		return true
	}
	for _, event := range pending {
		threadID, eventTurnID := eventIDs(event.Params)
		if threadID == a.threadID && eventTurnID == turnID {
			r.routeAttempt(a, event)
		}
	}
	select {
	case a.turnChanged <- struct{}{}:
	default:
	}
	return true
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func (r *Runtime) interruptAndFail(gen *generation, a *attempt, reason string) error {
	a.mu.Lock()
	if a.stopping {
		a.mu.Unlock()
		return interruptionError(reason)
	}
	a.stopping = true
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), r.cfg.Grace)
		defer cancel()
		_, _ = gen.client.Call(ctx, "turn/interrupt", map[string]any{"threadId": a.threadID, "turnId": a.turnID})
	}()
	grace := time.NewTimer(r.cfg.Grace)
	defer grace.Stop()
	select {
	case err := <-a.done:
		if err != nil && isTurnTimeout(reason) {
			return interruptionError(reason)
		}
		return err
	case <-grace.C:
		// A single uncooperative turn is a local command failure, not evidence
		// that the shared App Server generation is broken. Keep other channels
		// alive; a real process/protocol exit still goes through onGenerationExit.
		return interruptionError(reason)
	}
}

func isTurnTimeout(reason string) bool {
	return reason == "turn total timeout" || reason == "turn idle timeout"
}

func interruptionError(reason string) error {
	if isTurnTimeout(reason) {
		return fmt.Errorf("%s: %w", reason, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s", reason)
}

func (r *Runtime) dispatch(event Event) (string, error) {
	threadID, turnID := eventIDs(event.Params)
	r.mu.Lock()
	attempts := make([]*attempt, 0, len(r.attempts))
	for a := range r.attempts {
		attempts = append(attempts, a)
	}
	r.mu.Unlock()
	for _, a := range attempts {
		a.mu.Lock()
		sameThread := threadID != "" && a.threadID == threadID
		if sameThread && event.Method == "thread/tokenUsage/updated" && !a.finished {
			a.mu.Unlock()
			r.routeAttempt(a, event)
			return "attempt_routed", nil
		}
		// Several App Server protocol notifications (hooks, thread state and
		// future diagnostics) identify an active Thread but omit turnId. While
		// this attempt owns the thread, keep their complete business payload in
		// the same Trace instead of silently dropping it.
		if sameThread && turnID == "" && !a.finished {
			a.mu.Unlock()
			r.routeAttempt(a, event)
			return "attempt_routed", nil
		}
		if sameThread && a.goal {
			finished := a.finished
			_, knownTurn := a.knownTurns[turnID]
			bound := a.turnID != ""
			awaitingContinuation := a.awaitingContinuation
			goalTerminal := a.goalTerminal
			stopping := a.stopping
			a.mu.Unlock()
			if finished {
				continue
			}
			if event.Method == "thread/goal/updated" {
				r.routeAttempt(a, event)
				return "goal_routed", nil
			}
			if event.Method == "turn/started" && turnID != "" && !goalTerminal && !stopping && r.bindAttempt(a, turnID) {
				return "goal_continuation_bound", nil
			}
			if !bound && turnID != "" {
				a.mu.Lock()
				a.pending = append(a.pending, event)
				a.mu.Unlock()
				return "pending_turn_binding", nil
			}
			if !knownTurn && awaitingContinuation && !goalTerminal && !stopping && isContinuationTurnEvidence(event) && r.bindAttempt(a, turnID) {
				r.routeAttempt(a, event)
				return "goal_continuation_bound", nil
			}
			if knownTurn {
				r.routeAttempt(a, event)
				return "goal_routed", nil
			}
			continue
		}
		bound := a.turnID != ""
		sameTurn := bound && turnID == a.turnID
		if sameThread && !bound && turnID != "" {
			if isPresentationCandidate(event) {
				if len(a.pending) >= maxPresentationEvents || a.pendingBytes+len(event.Params) > maxPresentationBytes {
					a.presentationOver = true
					a.mu.Unlock()
					return "presentation_backpressure", nil
				}
				a.pendingBytes += len(event.Params)
			}
			a.pending = append(a.pending, event)
			a.mu.Unlock()
			return "pending_turn_binding", nil
		}
		a.mu.Unlock()
		if sameThread && sameTurn {
			r.routeAttempt(a, event)
			return "attempt_routed", nil
		}
	}
	if event.Method == "thread/goal/updated" && threadID != "" {
		r.mu.Lock()
		if r.earlyGoalUpdates == nil {
			r.earlyGoalUpdates = make(map[string]Event)
		}
		r.earlyGoalUpdates[threadID] = event
		r.mu.Unlock()
		return "pending_goal_preparation", nil
	}
	return "unrouted", nil
}

func (r *Runtime) routeAttempt(a *attempt, event Event) {
	if a.observation != nil {
		a.observation.RecordProtocolEvent(a.generation, event.Seq, event.Method, event.Params)
	}
	if event.Class == "server_request" {
		r.finish(a, fmt.Errorf("unsupported app server request: %s", event.Method))
		return
	}
	if event.Method == "thread/tokenUsage/updated" {
		if snapshot, ok := usageSnapshot(event.Params); ok {
			a.mu.Lock()
			a.lastTokenUsage = &snapshot
			a.mu.Unlock()
		}
	}
	if event.Method == "thread/goal/updated" && a.goal {
		var params struct {
			Goal struct {
				Status string `json:"status"`
			} `json:"goal"`
		}
		if json.Unmarshal(event.Params, &params) == nil {
			a.mu.Lock()
			switch params.Goal.Status {
			case "complete", "completed":
				a.goalTerminal, a.goalErr = true, nil
			case "paused", "budget_limited":
				a.goalTerminal, a.goalErr = true, fmt.Errorf("goal terminal status %q", params.Goal.Status)
			default:
				a.mu.Unlock()
				return
			}
			turnID, goalErr, awaitingContinuation := a.turnID, a.goalErr, a.awaitingContinuation
			a.mu.Unlock()
			if turnID == "" || awaitingContinuation {
				r.finish(a, goalErr)
			}
		}
		return
	}
	if event.Method == "turn/completed" {
		var params struct {
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Usage  *struct {
					InputTokens           int64 `json:"inputTokens"`
					OutputTokens          int64 `json:"outputTokens"`
					CachedInputTokens     int64 `json:"cachedInputTokens"`
					ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
					TotalTokens           int64 `json:"totalTokens"`
				} `json:"usage"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(event.Params, &params)
		a.mu.Lock()
		onTurnCompleted := a.onTurnCompleted
		lastSnapshot := a.lastTokenUsage
		goal, goalTerminal := a.goal, a.goalTerminal
		a.mu.Unlock()
		if onTurnCompleted != nil {
			completed := TurnCompleted{ThreadID: a.threadID, TurnID: params.Turn.ID, Status: params.Turn.Status}
			if params.Turn.Usage != nil {
				completed.Usage = &observability.Usage{InputTokens: params.Turn.Usage.InputTokens, OutputTokens: params.Turn.Usage.OutputTokens, CachedInputTokens: params.Turn.Usage.CachedInputTokens, ReasoningOutputTokens: params.Turn.Usage.ReasoningOutputTokens, TotalTokens: params.Turn.Usage.TotalTokens}
			} else if lastSnapshot != nil {
				snapshot := *lastSnapshot
				completed.SnapshotUsage = &snapshot
			}
			onTurnCompleted(completed)
		}
		turnErr := error(nil)
		if params.Turn.Status != "completed" {
			turnErr = fmt.Errorf("turn completed with status %q", params.Turn.Status)
		}
		if a.observation != nil && (!goal || goalTerminal) {
			var terminalUsage *observability.Usage
			if params.Turn.Usage != nil {
				usage := observability.Usage{InputTokens: params.Turn.Usage.InputTokens, OutputTokens: params.Turn.Usage.OutputTokens, CachedInputTokens: params.Turn.Usage.CachedInputTokens, ReasoningOutputTokens: params.Turn.Usage.ReasoningOutputTokens, TotalTokens: params.Turn.Usage.TotalTokens}
				terminalUsage = &usage
			} else if lastSnapshot != nil {
				snapshot := *lastSnapshot
				terminalUsage = &snapshot
			}
			a.observation.CloseProtocol(terminalUsage, turnErr)
		}
		if goal {
			a.mu.Lock()
			terminal, goalErr := a.goalTerminal, a.goalErr
			if !terminal {
				a.awaitingContinuation = true
			}
			a.mu.Unlock()
			if terminal {
				r.finish(a, goalErr)
			}
			return
		}
		if turnErr == nil {
			r.finish(a, nil)
		} else {
			r.finish(a, turnErr)
		}
		return
	}
	if event.Method == "item/completed" {
		var params struct {
			Item struct {
				ID    string `json:"id"`
				Type  string `json:"type"`
				Phase string `json:"phase"`
				Text  string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(event.Params, &params) == nil && params.Item.ID != "" && params.Item.Type == "agentMessage" && params.Item.Text != "" {
			a.mu.Lock()
			onItem := a.onItem
			finished := a.finished
			a.mu.Unlock()
			if !finished && onItem != nil && !onItem(CompletedItem{ID: params.Item.ID, Type: params.Item.Type, Phase: params.Item.Phase, Text: params.Item.Text}) {
				r.interruptAttempt(a, "presentation_backpressure")
			}
		}
	}
	select {
	case a.progress <- struct{}{}:
	default:
	}
}

func usageSnapshot(payload json.RawMessage) (observability.Usage, bool) {
	var params struct {
		TokenUsage struct {
			Total *struct {
				InputTokens           int64 `json:"inputTokens"`
				OutputTokens          int64 `json:"outputTokens"`
				CachedInputTokens     int64 `json:"cachedInputTokens"`
				ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
				TotalTokens           int64 `json:"totalTokens"`
			} `json:"total"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(payload, &params) != nil || params.TokenUsage.Total == nil {
		return observability.Usage{}, false
	}
	return observability.Usage{InputTokens: params.TokenUsage.Total.InputTokens, OutputTokens: params.TokenUsage.Total.OutputTokens, CachedInputTokens: params.TokenUsage.Total.CachedInputTokens, ReasoningOutputTokens: params.TokenUsage.Total.ReasoningOutputTokens, TotalTokens: params.TokenUsage.Total.TotalTokens}, true
}

// isContinuationTurnEvidence is limited to App Server events that identify an
// actual Turn. After a known Goal turn completes, some App Server versions may
// emit the next turn's item events before (or without) turn/started. Binding
// only at that completed boundary keeps unrelated same-thread events isolated.
func isContinuationTurnEvidence(event Event) bool {
	switch event.Method {
	case "turn/completed":
		var params struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		return json.Unmarshal(event.Params, &params) == nil && params.Turn.ID != ""
	case "item/started", "item/completed":
		var params struct {
			Item struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"item"`
		}
		return json.Unmarshal(event.Params, &params) == nil && params.Item.ID != "" && params.Item.Type == "agentMessage"
	default:
		return false
	}
}

func isPresentationCandidate(event Event) bool {
	if event.Method != "item/completed" {
		return false
	}
	var params struct {
		Item struct {
			ID, Type, Text string
		} `json:"item"`
	}
	return json.Unmarshal(event.Params, &params) == nil && params.Item.ID != "" && params.Item.Type == "agentMessage" && params.Item.Text != ""
}

func (r *Runtime) interruptAttempt(a *attempt, reason string) {
	r.mu.Lock()
	gen := r.current
	r.mu.Unlock()
	if gen == nil || gen.id != a.generation {
		return
	}
	a.mu.Lock()
	if a.finished || a.stopping || a.turnID == "" {
		a.mu.Unlock()
		return
	}
	a.stopping = true
	a.mu.Unlock()
	// Output backpressure is a deterministic local terminal condition. Finish
	// it immediately so the worker can close its projection, while the protocol
	// interrupt runs independently and never blocks the JSON-RPC reader.
	r.finish(a, errors.New(reason))
	go func(threadID, turnID string) {
		ctx, cancel := context.WithTimeout(context.Background(), r.cfg.Grace)
		defer cancel()
		_, _ = gen.client.Call(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID})
	}(a.threadID, a.turnID)
}

func (r *Runtime) finish(a *attempt, err error) {
	a.mu.Lock()
	if a.finished {
		a.mu.Unlock()
		return
	}
	bound := a.boundLocked()
	a.finished = true
	for _, state := range a.toolCalls {
		if !state.handlerStarted {
			state.terminal = true
		}
	}
	cancel := a.cancel
	observation := a.observation
	a.mu.Unlock()
	if observation != nil {
		observation.CloseProtocol(nil, err)
	}
	if cancel != nil {
		cancel()
	}
	a.boundOnce.Do(func() { close(bound) })
	a.done <- err
}

func (a *attempt) boundLocked() chan struct{} {
	if a.bound == nil {
		a.bound = make(chan struct{})
	}
	return a.bound
}
func (r *Runtime) failAttempts(generation uint64, err error) {
	r.mu.Lock()
	attempts := make([]*attempt, 0, len(r.attempts))
	for a := range r.attempts {
		if generation == 0 || a.generation == generation {
			attempts = append(attempts, a)
		}
	}
	r.mu.Unlock()
	for _, a := range attempts {
		r.finish(a, err)
	}
}
func (r *Runtime) snapshot(event Event) map[string]any {
	threadID, turnID, itemID := eventIdentity(event.Params)
	snapshot := map[string]any{
		"generation":     nil,
		"app_id":         nil,
		"channel_key":    nil,
		"chat_group_id":  nil,
		"attempt_id":     nil,
		"thread_id":      nullableID(threadID),
		"turn_id":        nullableID(turnID),
		"item_id":        nullableID(itemID),
		"json_rpc_class": event.Class,
		"method":         nullableID(event.Method),
	}
	r.mu.Lock()
	if r.current != nil {
		snapshot["generation"] = r.current.id
	}
	for attempt := range r.attempts {
		attempt.mu.Lock()
		matches := attempt.threadID == threadID && (attempt.turnID == "" || turnID == "" || attempt.turnID == turnID)
		route := attempt.route
		attempt.mu.Unlock()
		if matches {
			snapshot["app_id"], snapshot["channel_key"], snapshot["chat_group_id"], snapshot["attempt_id"] = route.AppID, route.ChannelKey, route.ChatGroupID, route.AttemptID
			break
		}
	}
	r.mu.Unlock()
	return snapshot
}
func (r *Runtime) generationForEvent() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil {
		return 0
	}
	return r.current.id
}

func eventIDs(raw json.RawMessage) (string, string) {
	threadID, turnID, _ := eventIdentity(raw)
	return threadID, turnID
}

func eventIdentity(raw json.RawMessage) (string, string, string) {
	var params struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		ItemID   string `json:"itemId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
		Item struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
		} `json:"item"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return "", "", ""
	}
	if params.ThreadID == "" {
		params.ThreadID = params.Item.ThreadID
	}
	if params.TurnID == "" {
		params.TurnID = params.Item.TurnID
	}
	if params.Turn.ID != "" {
		params.TurnID = params.Turn.ID
	}
	if params.ItemID == "" {
		params.ItemID = params.Item.ID
	}
	return params.ThreadID, params.TurnID, params.ItemID
}

func nullableID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

type processConn struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cmd    *exec.Cmd
	once   sync.Once
}

func startProcess(command string) (io.ReadWriteCloser, error) {
	cmd := exec.Command(command, "app-server", "--stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &processConn{stdin: stdin, stdout: stdout, cmd: cmd}, nil
}
func (p *processConn) Read(b []byte) (int, error)  { return p.stdout.Read(b) }
func (p *processConn) Write(b []byte) (int, error) { return p.stdin.Write(b) }
func (p *processConn) Close() error {
	p.once.Do(func() {
		_ = p.stdin.Close()
		_ = p.stdout.Close()
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		_ = p.cmd.Wait()
	})
	return nil
}
