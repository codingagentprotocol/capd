package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/codingagentprotocol/capd/internal/proc"
)

// rpcClient drives one `codex app-server` subprocess over stdio JSON-RPC.
// It is shared by every codex session: threads are multiplexed on a single
// connection, exactly like codex's own desktop app does.
type rpcClient struct {
	proc   *proc.Proc
	cancel context.CancelFunc

	writeMu sync.Mutex
	nextID  atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan rpcResult
	dead    bool

	// onNotify receives server notifications; onServerReq receives
	// server-initiated requests (approvals). onDead fires once when the
	// subprocess exits. All are called from the read loop — handlers must
	// not block.
	onNotify    func(method string, params json.RawMessage)
	onServerReq func(id json.RawMessage, method string, params json.RawMessage)
	onDead      func()
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func startRPC(env []string, onNotify func(string, json.RawMessage), onServerReq func(json.RawMessage, string, json.RawMessage), onDead func()) (*rpcClient, error) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := proc.Start(ctx, proc.Spec{Bin: binPath(), Args: []string{"app-server"}, Env: env})
	if err != nil {
		cancel()
		return nil, err
	}
	c := &rpcClient{
		proc:        p,
		cancel:      cancel,
		pending:     make(map[int64]chan rpcResult),
		onNotify:    onNotify,
		onServerReq: onServerReq,
		onDead:      onDead,
	}
	go c.readLoop()
	return c, nil
}

func (c *rpcClient) readLoop() {
	for line := range c.proc.Lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		switch {
		case msg.Method != "" && len(msg.ID) > 0: // server → client request
			if c.onServerReq != nil {
				c.onServerReq(msg.ID, msg.Method, msg.Params)
			}
		case msg.Method != "": // notification
			if c.onNotify != nil {
				c.onNotify(msg.Method, msg.Params)
			}
		case len(msg.ID) > 0: // response to one of our calls
			var id int64
			if err := json.Unmarshal(msg.ID, &id); err != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				res := rpcResult{result: msg.Result}
				if msg.Error != nil {
					res.err = fmt.Errorf("codex app-server: %s", msg.Error.Message)
				}
				ch <- res
			}
		}
	}
	// Process gone: fail everything in flight, then announce the death.
	c.mu.Lock()
	c.dead = true
	for id, ch := range c.pending {
		ch <- rpcResult{err: errors.New("codex app-server exited")}
		delete(c.pending, id)
	}
	c.mu.Unlock()
	go c.proc.Wait()
	if c.onDead != nil {
		c.onDead()
	}
}

// Call issues a request and waits for its response.
func (c *rpcClient) Call(ctx context.Context, method string, params any, result any) error {
	id := c.nextID.Add(1)
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}

	ch := make(chan rpcResult, 1)
	c.mu.Lock()
	if c.dead {
		c.mu.Unlock()
		return errors.New("codex app-server exited")
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(data); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}

	select {
	case res := <-ch:
		if res.err != nil {
			return res.err
		}
		if result != nil && len(res.result) > 0 {
			return json.Unmarshal(res.result, result)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}

// Respond answers a server-initiated request (approvals).
func (c *rpcClient) Respond(id json.RawMessage, result any) error {
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil {
		return err
	}
	return c.write(data)
}

func (c *rpcClient) write(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.proc.Write(append(data, '\n'))
}

func (c *rpcClient) Alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.dead
}

func (c *rpcClient) Kill() {
	c.cancel()
	go c.proc.Wait()
}
