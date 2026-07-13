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
	cfg            Config
	start          starter
	mu             sync.Mutex
	availability   Availability
	closed         bool
	current        *generation
	nextGeneration uint64
	attempts       map[*attempt]struct{}
	timeline       *Timeline
}

type generation struct {
	id     uint64
	client *Client
}
type attempt struct {
	mu               sync.Mutex
	generation       uint64
	threadID, turnID string
	pending          []Event
	pendingBytes     int
	presentationOver bool
	done             chan error
	finished         bool
	stopping         bool
	progress         chan struct{}
	route            RouteMetadata
	onItem           func(CompletedItem) bool
	toolHandler      ToolHandler
	ctx              context.Context
	cancel           context.CancelFunc
	actionSlots      chan struct{}
	bound            chan struct{}
	boundOnce        sync.Once
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
	return &Runtime{cfg: cfg, start: start, availability: Unavailable, attempts: make(map[*attempt]struct{})}
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

func (r *Runtime) StartTurn(ctx context.Context, threadID string, params TurnStartParams) (time.Time, error) {
	gen, err := r.client()
	if err != nil {
		return time.Time{}, err
	}
	attemptCtx, attemptCancel := context.WithCancel(ctx)
	a := &attempt{generation: gen.id, threadID: threadID, done: make(chan error, 1), progress: make(chan struct{}, 1), route: params.Route, onItem: params.OnItem, toolHandler: params.ToolHandler, ctx: attemptCtx, cancel: attemptCancel, actionSlots: make(chan struct{}, 4), bound: make(chan struct{})}
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
	responseCtx, cancel := context.WithTimeout(ctx, r.cfg.RPCTimeout)
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
		handlerCtx := a.ctx
		a.mu.Unlock()
		if sameThread && !bound {
			<-wait
			a.mu.Lock()
			matches = a.turnID == call.TurnID && !a.finished
			handler = a.toolHandler
			handlerCtx = a.ctx
			a.mu.Unlock()
		}
		if !matches {
			continue
		}
		if handler == nil {
			return ToolResult{Success: false, ContentItems: []ToolContentItem{{Type: "inputText", Text: "tool is unavailable for this turn"}}}, nil
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
		return handler(handlerCtx, call)
	}
	return ToolResult{Success: false, ContentItems: []ToolContentItem{{Type: "inputText", Text: "tool request does not match the active turn"}}}, nil
}

func (r *Runtime) bindAttempt(a *attempt, turnID string) {
	a.mu.Lock()
	bound := a.boundLocked()
	a.turnID = turnID
	pending := a.pending
	presentationOver := a.presentationOver
	a.pending = nil
	a.mu.Unlock()
	a.boundOnce.Do(func() { close(bound) })
	if presentationOver {
		r.interruptAttempt(a, "presentation_backpressure")
		return
	}
	for _, event := range pending {
		threadID, eventTurnID := eventIDs(event.Params)
		if threadID == a.threadID && eventTurnID == turnID {
			r.routeAttempt(a, event)
		}
	}
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
		gen.client.Close()
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
	return "unrouted", nil
}

func (r *Runtime) routeAttempt(a *attempt, event Event) {
	if event.Class == "server_request" {
		r.finish(a, fmt.Errorf("unsupported app server request: %s", event.Method))
		return
	}
	if event.Method == "turn/completed" {
		var params struct {
			Turn struct {
				Status string `json:"status"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(event.Params, &params)
		if params.Turn.Status == "completed" {
			r.finish(a, nil)
		} else {
			r.finish(a, fmt.Errorf("turn completed with status %q", params.Turn.Status))
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
	cancel := a.cancel
	a.mu.Unlock()
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
