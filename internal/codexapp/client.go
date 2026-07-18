package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

var ErrClientClosed = errors.New("app server client closed")

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Event struct {
	Seq    uint64
	Class  string
	Method string
	ID     json.RawMessage
	Params json.RawMessage
	Raw    []byte
}

type Observer interface {
	RecordRaw([]byte) (uint64, error)
	BeforeDispatch(*Event) error
	AfterDispatch(*Event, string, error) error
	ProtocolError(uint64, error) error
}
type Dispatcher func(Event) (string, error)
type ServerRequestHandler func(Event) (any, error)
type ServerResponseObserver func(Event, error)

type Client struct {
	conn           io.ReadWriteCloser
	writeMu        sync.Mutex
	mu             sync.Mutex
	pending        map[string]chan response
	nextID         atomic.Uint64
	eventSeq       atomic.Uint64
	done           chan struct{}
	closeOnce      sync.Once
	observer       Observer
	dispatch       Dispatcher
	server         ServerRequestHandler
	serverResponse ServerResponseObserver
}

// SetServerResponseObserver observes the terminal JSON-RPC write for server
// requests. It is intentionally separate from the handler: a successful tool
// handler is not a successful protocol response until stdout accepts it.
func (c *Client) SetServerResponseObserver(observer ServerResponseObserver) {
	c.mu.Lock()
	c.serverResponse = observer
	c.mu.Unlock()
}

func NewClient(conn io.ReadWriteCloser, observer Observer, dispatch Dispatcher, server ...ServerRequestHandler) *Client {
	c := &Client{conn: conn, pending: make(map[string]chan response), done: make(chan struct{}), observer: observer, dispatch: dispatch}
	if len(server) > 0 {
		c.server = server[0]
	}
	go c.readLoop()
	return c
}

func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) Close() {
	c.closeOnce.Do(func() {
		_ = c.conn.Close()
		c.mu.Lock()
		for _, pending := range c.pending {
			close(pending)
		}
		c.pending = map[string]chan response{}
		c.mu.Unlock()
		close(c.done)
	})
}

func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	idRaw, _ := json.Marshal(id)
	paramsRaw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal %s params: %w", method, err)
	}
	request := Request{JSONRPC: "2.0", ID: idRaw, Method: method, Params: paramsRaw}
	result := make(chan response, 1)
	key := string(idRaw)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return nil, ErrClientClosed
	default:
	}
	c.pending[key] = result
	c.mu.Unlock()
	if err := c.write(request); err != nil {
		c.removePending(key)
		return nil, err
	}
	select {
	case reply, ok := <-result:
		if !ok {
			return nil, ErrClientClosed
		}
		if reply.Error != nil {
			return nil, fmt.Errorf("app server %s: %s", method, reply.Error.Message)
		}
		return reply.Result, nil
	case <-ctx.Done():
		c.removePending(key)
		return nil, ctx.Err()
	case <-c.done:
		return nil, ErrClientClosed
	}
}

func (c *Client) removePending(key string) { c.mu.Lock(); delete(c.pending, key); c.mu.Unlock() }

func (c *Client) write(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err = c.conn.Write(append(encoded, '\n')); err != nil {
		c.Close()
		return fmt.Errorf("write app server request: %w", err)
	}
	return nil
}

func (c *Client) readLoop() {
	scanner := bufio.NewScanner(c.conn)
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 8*1024*1024)
	for scanner.Scan() {
		c.handle(append([]byte(nil), scanner.Bytes()...))
	}
	c.Close()
}

func (c *Client) handle(raw []byte) {
	seq := c.eventSeq.Add(1)
	if c.observer != nil {
		observedSeq, err := c.observer.RecordRaw(raw)
		if err != nil {
			c.Close()
			return
		}
		if observedSeq != 0 {
			seq = observedSeq
		}
	}
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		if c.observer != nil {
			_ = c.observer.ProtocolError(seq, fmt.Errorf("decode JSON-RPC: %w", err))
		}
		c.Close()
		return
	}
	event := Event{Seq: seq, Method: probe.Method, ID: probe.ID, Params: probe.Params, Raw: raw}
	if probe.Method != "" {
		if len(probe.ID) == 0 || string(probe.ID) == "null" {
			event.Class = "notification"
		} else {
			event.Class = "server_request"
		}
	} else if probe.Error != nil {
		event.Class = "jsonrpc_error"
	} else {
		event.Class = "response"
	}
	if c.observer != nil {
		if err := c.observer.BeforeDispatch(&event); err != nil {
			c.Close()
			return
		}
	}
	result, dispatchErr := c.dispatchEvent(event, probe.Result, probe.Error)
	if c.observer != nil {
		if err := c.observer.AfterDispatch(&event, result, dispatchErr); err != nil {
			c.Close()
		}
	}
}

func (c *Client) dispatchEvent(event Event, result json.RawMessage, rpcErr *rpcError) (string, error) {
	if event.Class == "response" || event.Class == "jsonrpc_error" {
		key := string(event.ID)
		c.mu.Lock()
		pending := c.pending[key]
		delete(c.pending, key)
		c.mu.Unlock()
		if pending == nil {
			return "unmatched_response", nil
		}
		pending <- response{ID: event.ID, Result: result, Error: rpcErr}
		return "response_delivered", nil
	}
	if event.Class == "server_request" {
		if c.server == nil {
			err := c.write(response{JSONRPC: "2.0", ID: event.ID, Error: &rpcError{Code: -32001, Message: "unsupported server request"}})
			c.notifyServerResponse(event, err)
		} else {
			go c.respondServerRequest(event)
		}
		return "server_request_dispatched", nil
	}
	if c.dispatch == nil {
		return "unrouted", nil
	}
	return c.dispatch(event)
}

func (c *Client) respondServerRequest(event Event) {
	result, err := c.server(event)
	var writeErr error
	if err != nil {
		writeErr = c.write(response{JSONRPC: "2.0", ID: event.ID, Error: &rpcError{Code: -32000, Message: "server request failed"}})
	} else {
		writeErr = c.write(response{JSONRPC: "2.0", ID: event.ID, Result: mustJSON(result)})
	}
	c.notifyServerResponse(event, writeErr)
}

func (c *Client) notifyServerResponse(event Event, writeErr error) {
	c.mu.Lock()
	observer := c.serverResponse
	c.mu.Unlock()
	if observer != nil {
		observer(event, writeErr)
	}
}

func ReadRequest(reader io.Reader) (Request, error) {
	line, err := bufio.NewReader(reader).ReadBytes('\n')
	if err != nil {
		return Request{}, err
	}
	var request Request
	if err := json.Unmarshal(line, &request); err != nil {
		return Request{}, err
	}
	return request, nil
}

func WriteResponse(writer io.Writer, id json.RawMessage, result any) error {
	encoded, err := json.Marshal(response{JSONRPC: "2.0", ID: id, Result: mustJSON(result)})
	if err != nil {
		return err
	}
	_, err = writer.Write(append(encoded, '\n'))
	return err
}

func mustJSON(value any) json.RawMessage { encoded, _ := json.Marshal(value); return encoded }
