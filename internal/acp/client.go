package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

var ErrClosed = errors.New("ACP connection closed")

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("ACP JSON-RPC error %d: %s", e.Code, e.Message)
}

type Notification struct {
	Method string
	Params json.RawMessage
}

type RequestHandler func(context.Context, string, json.RawMessage) (any, *RPCError)

type Client struct {
	ctx    context.Context
	reader io.Reader
	writer io.Writer
	handle RequestHandler

	nextID  atomic.Int64
	write   sync.Mutex
	mu      sync.Mutex
	pending map[int64]chan response

	notifications chan Notification
	closed        chan struct{}
	closeOnce     sync.Once
	errMu         sync.RWMutex
	err           error
}

type response struct {
	result json.RawMessage
	err    error
}

type wireMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func NewClient(ctx context.Context, reader io.Reader, writer io.Writer, handler RequestHandler) *Client {
	client := &Client{
		ctx: ctx, reader: reader, writer: writer, handle: handler,
		pending:       make(map[int64]chan response),
		notifications: make(chan Notification, 512),
		closed:        make(chan struct{}),
	}
	go client.readLoop()
	return client
}

func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)
	responseCh := make(chan response, 1)
	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		return c.Err()
	default:
	}
	c.pending[id] = responseCh
	c.mu.Unlock()

	message := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if err := c.send(message); err != nil {
		c.removePending(id)
		c.fail(err)
		return err
	}

	select {
	case response := <-responseCh:
		if response.err != nil {
			return response.err
		}
		if result == nil || len(response.result) == 0 || string(response.result) == "null" {
			return nil
		}
		if err := json.Unmarshal(response.result, result); err != nil {
			return fmt.Errorf("decode %s response: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	case <-c.closed:
		c.removePending(id)
		return c.Err()
	}
}

func (c *Client) Notify(method string, params any) error {
	select {
	case <-c.closed:
		return c.Err()
	default:
	}
	return c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *Client) Notifications() <-chan Notification { return c.notifications }
func (c *Client) Closed() <-chan struct{}            { return c.closed }

func (c *Client) Err() error {
	c.errMu.RLock()
	defer c.errMu.RUnlock()
	if c.err == nil {
		return ErrClosed
	}
	return c.err
}

func (c *Client) Close(err error) {
	if err == nil {
		err = ErrClosed
	}
	c.fail(err)
}

func (c *Client) send(message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode ACP message: %w", err)
	}
	payload = append(payload, '\n')
	c.write.Lock()
	defer c.write.Unlock()
	if _, err := c.writer.Write(payload); err != nil {
		return fmt.Errorf("write ACP message: %w", err)
	}
	return nil
}

func (c *Client) readLoop() {
	scanner := bufio.NewScanner(c.reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var message wireMessage
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			c.fail(fmt.Errorf("decode ACP message: %w", err))
			return
		}
		if message.JSONRPC != "2.0" {
			c.fail(fmt.Errorf("unsupported JSON-RPC version %q", message.JSONRPC))
			return
		}
		if message.Method != "" {
			if hasID(message.ID) {
				go c.handleRequest(message)
				continue
			}
			select {
			case c.notifications <- Notification{Method: message.Method, Params: message.Params}:
			case <-c.closed:
				return
			case <-c.ctx.Done():
				c.fail(c.ctx.Err())
				return
			}
			continue
		}
		if !hasID(message.ID) {
			c.fail(errors.New("ACP response is missing id"))
			return
		}
		id, err := parseNumericID(message.ID)
		if err != nil {
			c.fail(err)
			return
		}
		c.mu.Lock()
		pending, ok := c.pending[id]
		if ok {
			delete(c.pending, id)
		}
		c.mu.Unlock()
		if !ok {
			continue
		}
		if message.Error != nil {
			pending <- response{err: message.Error}
		} else {
			pending <- response{result: message.Result}
		}
	}
	if err := scanner.Err(); err != nil {
		c.fail(fmt.Errorf("read ACP stream: %w", err))
		return
	}
	c.fail(io.EOF)
}

func (c *Client) handleRequest(message wireMessage) {
	var result any
	var rpcErr *RPCError
	if c.handle == nil {
		rpcErr = &RPCError{Code: -32601, Message: "method not supported"}
	} else {
		result, rpcErr = c.handle(c.ctx, message.Method, message.Params)
	}
	response := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message.ID)}
	if rpcErr != nil {
		response["error"] = rpcErr
	} else {
		response["result"] = result
	}
	if err := c.send(response); err != nil {
		c.fail(err)
	}
}

func (c *Client) fail(err error) {
	c.closeOnce.Do(func() {
		c.errMu.Lock()
		c.err = fmt.Errorf("%w: %v", ErrClosed, err)
		c.errMu.Unlock()
		close(c.closed)

		c.mu.Lock()
		pending := c.pending
		c.pending = make(map[int64]chan response)
		c.mu.Unlock()
		for _, ch := range pending {
			ch <- response{err: c.Err()}
		}
	})
}

func (c *Client) removePending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func hasID(id json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(id))
	return trimmed != "" && trimmed != "null"
}

func parseNumericID(raw json.RawMessage) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unsupported ACP response id %s", raw)
	}
	return id, nil
}
