package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// appServer multiplexes codex sessions onto one `codex app-server` process —
// the same engine codex's desktop app embeds. Notifications and approval
// requests are routed to sessions by threadId.
type appServer struct {
	mu       sync.Mutex
	client   *rpcClient
	sessions map[string]*appSession // threadId → session
}

func (as *appServer) ensureClient() (*rpcClient, error) {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.client != nil && as.client.Alive() {
		return as.client, nil
	}
	c, err := startRPC(as.routeNotification, as.routeServerRequest)
	if err != nil {
		return nil, err
	}
	if err := c.Call(context.Background(), "initialize", map[string]any{
		"clientInfo": map[string]any{"name": "capd", "title": "capd", "version": "0.1"},
	}, nil); err != nil {
		c.Kill()
		return nil, fmt.Errorf("initialize app-server: %w", err)
	}
	as.client = c
	as.sessions = make(map[string]*appSession)
	return c, nil
}

func (as *appServer) register(threadID string, s *appSession) {
	as.mu.Lock()
	as.sessions[threadID] = s
	as.mu.Unlock()
}

func (as *appServer) unregister(threadID string) {
	as.mu.Lock()
	delete(as.sessions, threadID)
	as.mu.Unlock()
}

func (as *appServer) lookup(params json.RawMessage) *appSession {
	var probe struct {
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(params, &probe); err != nil || probe.ThreadID == "" {
		return nil
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.sessions[probe.ThreadID]
}

func (as *appServer) routeNotification(method string, params json.RawMessage) {
	if s := as.lookup(params); s != nil {
		s.handleNotification(method, params)
	}
}

func (as *appServer) routeServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	if s := as.lookup(params); s != nil {
		s.handleServerRequest(id, method, params)
		return
	}
	// Unroutable request: deny rather than leave codex hanging.
	as.mu.Lock()
	c := as.client
	as.mu.Unlock()
	if c != nil {
		c.Respond(id, map[string]any{"decision": "reject"})
	}
}

// startAppSession starts (or resumes) a thread and returns the session.
func (as *appServer) startAppSession(ctx context.Context, opts adapter.SessionOpts) (*appSession, error) {
	c, err := as.ensureClient()
	if err != nil {
		return nil, err
	}

	params := map[string]any{}
	if opts.Cwd != "" {
		params["cwd"] = opts.Cwd
	}
	approval, sandbox := permissionMapping(opts.PermissionMode)
	params["approvalPolicy"] = approval
	params["sandbox"] = sandbox

	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if opts.Resume != "" {
		params["threadId"] = opts.Resume
		if err := c.Call(ctx, "thread/resume", params, &result); err != nil {
			return nil, err
		}
		if result.Thread.ID == "" {
			result.Thread.ID = opts.Resume
		}
	} else {
		if err := c.Call(ctx, "thread/start", params, &result); err != nil {
			return nil, err
		}
	}

	s := &appSession{
		owner:    as,
		client:   c,
		threadID: result.Thread.ID,
		events:   make(chan protocol.Event, 256),
	}
	as.register(s.threadID, s)
	s.emit(protocol.EventSessionStarted, map[string]any{
		"nativeSessionId": s.threadID, "threadId": s.threadID,
	})
	return s, nil
}

// permissionMapping maps CAP permission modes onto codex's approval policy
// and sandbox mode. Defaults are deliberately explicit — the user's
// ~/.codex/config.toml may be permissive, and capd sessions must not
// silently inherit that.
//
//	default     → read-only sandbox; every write/escalation needs approval
//	acceptEdits → workspace-write; outside-workspace actions need approval
//	full        → no sandbox, no approvals (the user opted in)
func permissionMapping(mode string) (approvalPolicy, sandbox string) {
	switch mode {
	case protocol.PermissionFull:
		return "never", "danger-full-access"
	case protocol.PermissionAcceptEdits:
		return "on-request", "workspace-write"
	default:
		return "on-request", "read-only"
	}
}

// appSession is one codex thread exposed as an adapter.Session.
type appSession struct {
	owner    *appServer
	client   *rpcClient
	threadID string
	events   chan protocol.Event

	mu        sync.Mutex
	turnID    string // current/last turn
	lastUsage map[string]any
	approvals map[string]json.RawMessage // approvalId → rpc request id
	closed    bool
}

func (s *appSession) Events() <-chan protocol.Event { return s.events }

func (s *appSession) emit(t protocol.EventType, data map[string]any) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	select {
	case s.events <- protocol.Event{Type: t, Data: data}:
	default: // session pump should always keep up; drop rather than deadlock the rpc read loop
	}
}

func (s *appSession) Send(ctx context.Context, prompt string) error {
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	err := s.client.Call(ctx, "turn/start", map[string]any{
		"threadId": s.threadID,
		"input":    []map[string]any{{"type": "text", "text": prompt}},
	}, &result)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.turnID = result.Turn.ID
	s.mu.Unlock()
	return nil
}

func (s *appSession) Steer(ctx context.Context, prompt string) error {
	s.mu.Lock()
	turnID := s.turnID
	s.mu.Unlock()
	if turnID == "" {
		return fmt.Errorf("no turn to steer")
	}
	return s.client.Call(ctx, "turn/steer", map[string]any{
		"threadId":       s.threadID,
		"expectedTurnId": turnID,
		"input":          []map[string]any{{"type": "text", "text": prompt}},
	}, nil)
}

func (s *appSession) Cancel() {
	s.mu.Lock()
	turnID := s.turnID
	s.mu.Unlock()
	if turnID == "" {
		return
	}
	s.client.Call(context.Background(), "turn/interrupt", map[string]any{
		"threadId": s.threadID, "turnId": turnID,
	}, nil)
}

func (s *appSession) Approve(_ context.Context, approvalID, decision string) error {
	s.mu.Lock()
	rpcID, ok := s.approvals[approvalID]
	delete(s.approvals, approvalID)
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown approval %q", approvalID)
	}
	return s.client.Respond(rpcID, map[string]any{"decision": translateDecision(decision)})
}

func translateDecision(d string) string {
	switch d {
	case protocol.DecisionApprove:
		return "accept"
	case protocol.DecisionApproveAlways:
		return "acceptForSession"
	default:
		return "reject"
	}
}

func (s *appSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.owner.unregister(s.threadID)
	close(s.events)
	return nil
}

// handleServerRequest turns codex approval requests into approval.needed.
func (s *appSession) handleServerRequest(id json.RawMessage, method string, params json.RawMessage) {
	var detail map[string]any
	json.Unmarshal(params, &detail)

	kind := "unknown"
	switch method {
	case "item/commandExecution/requestApproval":
		kind = "command"
	case "item/fileChange/requestApproval":
		kind = "fileChange"
	case "item/permissions/requestApproval":
		kind = "permissions"
	default:
		// A server request we do not understand: refuse it safely.
		s.client.Respond(id, map[string]any{"decision": "reject"})
		return
	}

	approvalID := fmt.Sprintf("a_%s", string(id))
	s.mu.Lock()
	if s.approvals == nil {
		s.approvals = make(map[string]json.RawMessage)
	}
	s.approvals[approvalID] = append(json.RawMessage(nil), id...)
	s.mu.Unlock()

	data := map[string]any{"approvalId": approvalID, "kind": kind}
	for k, v := range detail {
		if k != "threadId" && k != "turnId" {
			data[k] = v
		}
	}
	s.emit(protocol.EventApprovalNeeded, data)
}

// handleNotification translates codex app-server notifications to CAP events.
func (s *appSession) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "turn/started":
		var p struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		json.Unmarshal(params, &p)
		s.mu.Lock()
		s.turnID = p.Turn.ID
		s.mu.Unlock()

	case "item/agentMessage/delta":
		var p struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		json.Unmarshal(params, &p)
		s.emit(protocol.EventOutputText, map[string]any{"text": p.Delta, "delta": true, "itemId": p.ItemID})

	case "item/reasoning/textDelta", "item/reasoning/summaryTextDelta":
		var p struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		json.Unmarshal(params, &p)
		s.emit(protocol.EventOutputReasoning, map[string]any{"text": p.Delta, "delta": true, "itemId": p.ItemID})

	case "item/started", "item/completed":
		s.handleItem(method, params)

	case "item/commandExecution/outputDelta":
		var p struct {
			ItemID string `json:"itemId"`
			Chunk  string `json:"chunk"`
			Delta  string `json:"delta"`
		}
		json.Unmarshal(params, &p)
		out := p.Chunk
		if out == "" {
			out = p.Delta
		}
		s.emit(protocol.EventToolResult, map[string]any{"kind": "shell", "output": out, "delta": true, "itemId": p.ItemID})

	case "thread/tokenUsage/updated":
		var p struct {
			TokenUsage map[string]any `json:"tokenUsage"`
		}
		json.Unmarshal(params, &p)
		s.mu.Lock()
		s.lastUsage = p.TokenUsage
		s.mu.Unlock()

	case "account/rateLimits/updated":
		var p map[string]any
		json.Unmarshal(params, &p)
		s.emit(protocol.EventUsageUpdated, p)

	case "turn/completed", "turn/failed":
		var p struct {
			Turn struct {
				Status string `json:"status"`
				Error  any    `json:"error"`
			} `json:"turn"`
		}
		json.Unmarshal(params, &p)
		s.mu.Lock()
		usage := s.lastUsage
		s.mu.Unlock()
		data := map[string]any{"ok": p.Turn.Status == "completed"}
		if usage != nil {
			data["usage"] = usage
		}
		if p.Turn.Error != nil {
			data["error"] = p.Turn.Error
		}
		s.emit(protocol.EventTaskDone, data)

	case "error":
		var p struct {
			Message string `json:"message"`
		}
		json.Unmarshal(params, &p)
		s.emit(protocol.EventError, map[string]any{"message": p.Message})
	}
}

// handleItem maps non-delta item lifecycle to tool.use / tool.result.
// agentMessage and reasoning completions are skipped: their content already
// streamed as deltas; clients that ignore deltas still get the final text in
// item data below.
func (s *appSession) handleItem(method string, params json.RawMessage) {
	var p struct {
		Item map[string]any `json:"item"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	itemType, _ := p.Item["type"].(string)

	switch itemType {
	case "agentMessage":
		if method == "item/completed" {
			text, _ := p.Item["text"].(string)
			s.emit(protocol.EventOutputText, map[string]any{"text": text, "final": true, "itemId": p.Item["id"]})
		}
	case "reasoning", "userMessage":
		// reasoning streamed as deltas; userMessage echoes our own input
	case "commandExecution":
		if method == "item/started" {
			s.emit(protocol.EventToolUse, map[string]any{"kind": "shell", "command": p.Item["command"], "item": p.Item})
		} else {
			s.emit(protocol.EventToolResult, map[string]any{
				"kind": "shell", "command": p.Item["command"],
				"output": p.Item["aggregatedOutput"], "exitCode": p.Item["exitCode"], "item": p.Item,
			})
		}
	default:
		if method == "item/started" {
			s.emit(protocol.EventToolUse, map[string]any{"kind": itemType, "item": p.Item})
		} else {
			s.emit(protocol.EventToolResult, map[string]any{"kind": itemType, "item": p.Item})
		}
	}
}
