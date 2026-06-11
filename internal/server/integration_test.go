package server

// Protocol-level integration suite: a real WS server and a scripted fake
// adapter exercise every method and error path without touching a real
// agent CLI. Live-agent smoke tests stay outside CI (see docs/testing.md).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/session"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// ---- scripted fake adapter ----

type scriptedAdapter struct {
	mu       sync.Mutex
	sessions []*scriptedSession
}

func (f *scriptedAdapter) ID() string { return "fake" }
func (f *scriptedAdapter) Probe(context.Context) (protocol.AgentInfo, error) {
	return protocol.AgentInfo{ID: "fake", Name: "Fake", Available: true}, nil
}
func (f *scriptedAdapter) Capabilities() protocol.AgentCapabilities {
	return protocol.AgentCapabilities{
		Streaming: true,
		Approvals: true,
		Steer:     true,
		Fork:      true,
		Rollback:  true,
		Review:    true,
		Images:    true,
		Usage:     true,
		Resume:    true,
	}
}
func (f *scriptedAdapter) StartSession(_ context.Context, opts adapter.SessionOpts) (adapter.Session, error) {
	s := &scriptedSession{opts: opts, events: make(chan protocol.Event, 64)}
	f.mu.Lock()
	f.sessions = append(f.sessions, s)
	f.mu.Unlock()
	return s, nil
}

type scriptedSession struct {
	opts   adapter.SessionOpts
	events chan protocol.Event

	mu       sync.Mutex
	steered  []string
	rolled   []int
	reviewed []protocol.ReviewTarget
	approved map[string]string
	closed   bool
}

func (s *scriptedSession) Send(_ context.Context, msg adapter.Message) error {
	prompt := msg.Prompt
	s.events <- protocol.Event{Type: protocol.EventSessionStarted, Data: map[string]any{"nativeSessionId": "fake-native-1"}}
	if strings.Contains(prompt, "need-approval") {
		s.events <- protocol.Event{Type: protocol.EventApprovalNeeded, Data: map[string]any{"approvalId": "ap_1", "kind": "command"}}
		return nil // task.done arrives after Approve
	}
	s.events <- protocol.Event{Type: protocol.EventOutputText, Data: map[string]any{"text": "echo:" + prompt}}
	s.events <- protocol.Event{Type: protocol.EventTaskDone, Data: map[string]any{"ok": true}}
	return nil
}

func (s *scriptedSession) Steer(_ context.Context, prompt string) error {
	s.mu.Lock()
	s.steered = append(s.steered, prompt)
	s.mu.Unlock()
	s.events <- protocol.Event{Type: protocol.EventOutputText, Data: map[string]any{"text": "steered:" + prompt}}
	return nil
}

func (s *scriptedSession) Approve(_ context.Context, id, decision string) error {
	s.mu.Lock()
	if s.approved == nil {
		s.approved = map[string]string{}
	}
	s.approved[id] = decision
	s.mu.Unlock()
	if id != "ap_1" {
		return fmt.Errorf("unknown approval %q", id)
	}
	s.events <- protocol.Event{Type: protocol.EventToolResult, Data: map[string]any{"output": "ran-after-" + decision}}
	s.events <- protocol.Event{Type: protocol.EventTaskDone, Data: map[string]any{"ok": true}}
	return nil
}

func (s *scriptedSession) Fork(context.Context) (adapter.Session, string, error) {
	forked := &scriptedSession{events: make(chan protocol.Event, 64)}
	forked.events <- protocol.Event{Type: protocol.EventSessionStarted, Data: map[string]any{"nativeSessionId": "fake-native-fork"}}
	return forked, "fake-native-fork", nil
}

func (s *scriptedSession) Rollback(_ context.Context, numTurns int) error {
	s.mu.Lock()
	s.rolled = append(s.rolled, numTurns)
	s.mu.Unlock()
	s.events <- protocol.Event{Type: protocol.EventOutputText, Data: map[string]any{"text": fmt.Sprintf("rolled:%d", numTurns)}}
	return nil
}

func (s *scriptedSession) Review(_ context.Context, target protocol.ReviewTarget) error {
	s.mu.Lock()
	s.reviewed = append(s.reviewed, target)
	s.mu.Unlock()
	s.events <- protocol.Event{Type: protocol.EventOutputText, Data: map[string]any{"text": "review:" + target.Type}}
	s.events <- protocol.Event{Type: protocol.EventTaskDone, Data: map[string]any{"ok": true}}
	return nil
}

func (s *scriptedSession) Cancel() {
	s.events <- protocol.Event{Type: protocol.EventTaskDone, Data: map[string]any{"ok": false, "canceled": true}}
}

func (s *scriptedSession) Events() <-chan protocol.Event { return s.events }
func (s *scriptedSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.events)
	}
	return nil
}

// ---- test client ----

type testClient struct {
	t      *testing.T
	conn   *websocket.Conn
	ctx    context.Context
	nextID int

	mu     sync.Mutex
	events []protocol.Event
	resps  map[int]*protocol.Response
}

func dialClient(t *testing.T, ts *httptest.Server, token string) *testClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	url := strings.Replace(ts.URL, "http://", "ws://", 1) + "?token=" + token
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	c := &testClient{t: t, conn: conn, ctx: ctx, resps: map[int]*protocol.Response{}}
	go c.readLoop()
	return c
}

func (c *testClient) readLoop() {
	for {
		_, data, err := c.conn.Read(c.ctx)
		if err != nil {
			return
		}
		var probe struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		json.Unmarshal(data, &probe)
		c.mu.Lock()
		if probe.Method == protocol.MethodEvent {
			var ev protocol.Event
			json.Unmarshal(probe.Params, &ev)
			c.events = append(c.events, ev)
		} else if probe.ID != nil {
			var resp protocol.Response
			json.Unmarshal(data, &resp)
			c.resps[*probe.ID] = &resp
		}
		c.mu.Unlock()
	}
}

func (c *testClient) call(method string, params any) *protocol.Response {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	p, _ := json.Marshal(params)
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage(p)})
	if err := c.conn.Write(c.ctx, websocket.MessageText, req); err != nil {
		c.t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		resp := c.resps[id]
		c.mu.Unlock()
		if resp != nil {
			return resp
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("no response to %s", method)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (c *testClient) waitEvent(typ protocol.EventType) protocol.Event {
	c.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		for _, ev := range c.events {
			if ev.Type == typ {
				c.mu.Unlock()
				return ev
			}
		}
		snapshot := append([]protocol.Event(nil), c.events...)
		c.mu.Unlock()
		if time.Now().After(deadline) {
			c.t.Fatalf("event %s never arrived; got %+v", typ, snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (c *testClient) mustResult(resp *protocol.Response, into any) {
	c.t.Helper()
	if resp.Error != nil {
		c.t.Fatalf("unexpected error: %v", resp.Error)
	}
	if into != nil {
		if err := json.Unmarshal(resp.Result, into); err != nil {
			c.t.Fatal(err)
		}
	}
}

func newIntegration(t *testing.T) (*httptest.Server, *scriptedAdapter) {
	t.Helper()
	fake := &scriptedAdapter{}
	reg := adapter.NewRegistry(fake)
	st, err := session.OpenStore(t.TempDir() + "/it.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(Options{
		Token: "it-token", Version: "it",
		Registry: reg, Sessions: session.NewManager(reg, st),
		Log: slog.New(slog.NewTextHandler(testWriter{t}, nil)),
	})
	ts := httptest.NewServer(http.HandlerFunc(s.handleWS))
	t.Cleanup(ts.Close)
	return ts, fake
}

func initialized(t *testing.T, ts *httptest.Server) *testClient {
	t.Helper()
	c := dialClient(t, ts, "it-token")
	resp := c.call(protocol.MethodInitialize, protocol.InitializeParams{ProtocolVersion: protocol.Version})
	c.mustResult(resp, nil)
	return c
}

// ---- the suite ----

func TestRejectsWrongToken(t *testing.T) {
	ts, _ := newIntegration(t)
	resp, err := http.Get(ts.URL + "?token=wrong")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestVersionNegotiationRejected(t *testing.T) {
	ts, _ := newIntegration(t)
	c := dialClient(t, ts, "it-token")
	resp := c.call(protocol.MethodInitialize, protocol.InitializeParams{ProtocolVersion: "99.0"})
	if resp.Error == nil || resp.Error.Code != protocol.CodeVersionUnsupported {
		t.Fatalf("want version error, got %+v", resp)
	}
}

func TestRequiresInitializeFirst(t *testing.T) {
	ts, _ := newIntegration(t)
	c := dialClient(t, ts, "it-token")
	resp := c.call(protocol.MethodSessionList, struct{}{})
	if resp.Error == nil || resp.Error.Code != protocol.CodeInvalidRequest {
		t.Fatalf("want invalid request before initialize, got %+v", resp)
	}
}

func TestUnknownMethodAndSession(t *testing.T) {
	ts, _ := newIntegration(t)
	c := initialized(t, ts)
	if resp := c.call("no/such/method", struct{}{}); resp.Error == nil || resp.Error.Code != protocol.CodeMethodNotFound {
		t.Fatalf("want method-not-found, got %+v", resp)
	}
	if resp := c.call(protocol.MethodTaskSend, protocol.TaskSendParams{SessionID: "s_nope", Prompt: "x"}); resp.Error == nil || resp.Error.Code != protocol.CodeSessionNotFound {
		t.Fatalf("want session-not-found, got %+v", resp)
	}
}

func TestAgentsListReportsCapabilities(t *testing.T) {
	ts, _ := newIntegration(t)
	c := initialized(t, ts)

	var list protocol.AgentsListResult
	c.mustResult(c.call(protocol.MethodAgentsList, struct{}{}), &list)
	if len(list.Agents) != 1 {
		t.Fatalf("agents = %+v", list.Agents)
	}
	got := list.Agents[0].Capabilities
	if !got.Streaming || !got.Approvals || !got.Steer || !got.Fork || !got.Rollback || !got.Review || !got.Images || !got.Usage || !got.Resume {
		t.Fatalf("capabilities = %+v", got)
	}
}

func TestSessionLifecycleAndReplay(t *testing.T) {
	ts, _ := newIntegration(t)
	c := initialized(t, ts)

	var created protocol.SessionCreateResult
	c.mustResult(c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{AgentID: "fake"}), &created)
	c.mustResult(c.call(protocol.MethodTaskSend, protocol.TaskSendParams{SessionID: created.SessionID, Prompt: "hello"}), nil)

	if ev := c.waitEvent(protocol.EventOutputText); ev.Data["text"] != "echo:hello" {
		t.Fatalf("text = %v", ev.Data["text"])
	}
	c.waitEvent(protocol.EventTaskDone)

	// A second client attaches and replays the full history by seq.
	c2 := initialized(t, ts)
	var attached protocol.SessionAttachResult
	c2.mustResult(c2.call(protocol.MethodSessionAttach, protocol.SessionAttachParams{SessionID: created.SessionID}), &attached)
	if attached.NextSeq < 3 {
		t.Fatalf("nextSeq = %d, want >= 3", attached.NextSeq)
	}
	if ev := c2.waitEvent(protocol.EventOutputText); ev.Data["text"] != "echo:hello" {
		t.Fatalf("replayed text = %v", ev.Data["text"])
	}

	// Close, then the session is gone for new work.
	c.mustResult(c.call(protocol.MethodSessionClose, protocol.SessionCloseParams{SessionID: created.SessionID}), nil)
	if resp := c.call(protocol.MethodTaskSend, protocol.TaskSendParams{SessionID: created.SessionID, Prompt: "x"}); resp.Error == nil {
		t.Fatalf("send after close should fail, got %+v", resp)
	}
}

func TestRejectsInvalidPermissionMode(t *testing.T) {
	ts, _ := newIntegration(t)
	c := initialized(t, ts)
	resp := c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{
		AgentID: "fake", PermissionMode: "surprise-me",
	})
	if resp.Error == nil || resp.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("want invalid params, got %+v", resp)
	}
}

func TestForkRollbackAndReview(t *testing.T) {
	ts, fake := newIntegration(t)
	c := initialized(t, ts)

	var created protocol.SessionCreateResult
	c.mustResult(c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{AgentID: "fake"}), &created)

	var forked protocol.SessionForkResult
	c.mustResult(c.call(protocol.MethodSessionFork, protocol.SessionForkParams{SessionID: created.SessionID}), &forked)
	if forked.SessionID == "" || forked.SessionID == created.SessionID {
		t.Fatalf("forked session id = %q", forked.SessionID)
	}
	if ev := c.waitEvent(protocol.EventSessionStarted); ev.Data["nativeSessionId"] != "fake-native-fork" {
		t.Fatalf("fork event = %+v", ev)
	}

	c.mustResult(c.call(protocol.MethodSessionRollback, protocol.SessionRollbackParams{SessionID: created.SessionID, NumTurns: 2}), nil)
	fake.mu.Lock()
	sess := fake.sessions[0]
	fake.mu.Unlock()
	sess.mu.Lock()
	rolled := append([]int(nil), sess.rolled...)
	sess.mu.Unlock()
	if len(rolled) != 1 || rolled[0] != 2 {
		t.Fatalf("rolled = %v", rolled)
	}
	if resp := c.call(protocol.MethodSessionRollback, protocol.SessionRollbackParams{SessionID: created.SessionID, NumTurns: 0}); resp.Error == nil || resp.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("want invalid numTurns, got %+v", resp)
	}

	target := protocol.ReviewTarget{Type: "branch", Branch: "main"}
	c.mustResult(c.call(protocol.MethodTaskReview, protocol.TaskReviewParams{SessionID: created.SessionID, Target: target}), nil)
	sess.mu.Lock()
	reviewed := append([]protocol.ReviewTarget(nil), sess.reviewed...)
	sess.mu.Unlock()
	if len(reviewed) != 1 || reviewed[0] != target {
		t.Fatalf("reviewed = %+v", reviewed)
	}
	c.waitEvent(protocol.EventTaskDone)
}

func TestSteerAndCancel(t *testing.T) {
	ts, fake := newIntegration(t)
	c := initialized(t, ts)

	var created protocol.SessionCreateResult
	c.mustResult(c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{AgentID: "fake"}), &created)
	c.mustResult(c.call(protocol.MethodTaskSend, protocol.TaskSendParams{SessionID: created.SessionID, Prompt: "hello"}), nil)
	c.mustResult(c.call(protocol.MethodTaskSteer, protocol.TaskSteerParams{SessionID: created.SessionID, Prompt: "left"}), nil)

	fake.mu.Lock()
	sess := fake.sessions[0]
	fake.mu.Unlock()
	sess.mu.Lock()
	steered := append([]string(nil), sess.steered...)
	sess.mu.Unlock()
	if len(steered) != 1 || steered[0] != "left" {
		t.Fatalf("steered = %v", steered)
	}

	c.mustResult(c.call(protocol.MethodTaskCancel, protocol.TaskCancelParams{SessionID: created.SessionID}), nil)
	c.waitEvent(protocol.EventTaskDone)
}

func TestApprovalRoundTrip(t *testing.T) {
	ts, _ := newIntegration(t)
	c := initialized(t, ts)

	var created protocol.SessionCreateResult
	c.mustResult(c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{AgentID: "fake"}), &created)
	c.mustResult(c.call(protocol.MethodTaskSend, protocol.TaskSendParams{SessionID: created.SessionID, Prompt: "need-approval"}), nil)

	ev := c.waitEvent(protocol.EventApprovalNeeded)
	approvalID, _ := ev.Data["approvalId"].(string)
	c.mustResult(c.call(protocol.MethodApprovalReply, protocol.ApprovalReplyParams{
		SessionID: created.SessionID, ApprovalID: approvalID, Decision: protocol.DecisionApprove,
	}), nil)

	if res := c.waitEvent(protocol.EventToolResult); res.Data["output"] != "ran-after-approve" {
		t.Fatalf("tool result = %v", res.Data)
	}
	c.waitEvent(protocol.EventTaskDone)

	// Replying to a bogus approval id errors but does not kill anything.
	if resp := c.call(protocol.MethodApprovalReply, protocol.ApprovalReplyParams{
		SessionID: created.SessionID, ApprovalID: "nope", Decision: protocol.DecisionDeny,
	}); resp.Error == nil {
		t.Fatal("bogus approval id should error")
	}
}

func TestSessionList(t *testing.T) {
	ts, _ := newIntegration(t)
	c := initialized(t, ts)

	var created protocol.SessionCreateResult
	c.mustResult(c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{AgentID: "fake"}), &created)

	var list protocol.SessionListResult
	c.mustResult(c.call(protocol.MethodSessionList, struct{}{}), &list)
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != created.SessionID {
		t.Fatalf("list = %+v", list)
	}
	if list.Sessions[0].State != protocol.SessionStateLive {
		t.Fatalf("state = %s, want live", list.Sessions[0].State)
	}

	c.mustResult(c.call(protocol.MethodSessionClose, protocol.SessionCloseParams{SessionID: created.SessionID}), nil)
	c.mustResult(c.call(protocol.MethodSessionList, struct{}{}), &list)
	if list.Sessions[0].State != protocol.SessionStateEnded {
		t.Fatalf("state after close = %s, want ended", list.Sessions[0].State)
	}
}

func TestCloseStoredSession(t *testing.T) {
	ts, fake := newIntegration(t)
	c := initialized(t, ts)

	var created protocol.SessionCreateResult
	c.mustResult(c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{AgentID: "fake"}), &created)
	fake.mu.Lock()
	sess := fake.sessions[0]
	fake.mu.Unlock()
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	c.waitEvent(protocol.EventSessionEnded)

	var list protocol.SessionListResult
	c.mustResult(c.call(protocol.MethodSessionList, struct{}{}), &list)
	if len(list.Sessions) != 1 || list.Sessions[0].State != protocol.SessionStateStored {
		t.Fatalf("state after adapter close = %+v, want stored", list.Sessions)
	}

	c.mustResult(c.call(protocol.MethodSessionClose, protocol.SessionCloseParams{SessionID: created.SessionID}), nil)
	c.mustResult(c.call(protocol.MethodSessionList, struct{}{}), &list)
	if len(list.Sessions) != 1 || list.Sessions[0].State != protocol.SessionStateEnded {
		t.Fatalf("state after stored close = %+v, want ended", list.Sessions)
	}
}

func TestSessionHistory(t *testing.T) {
	ts, _ := newIntegration(t)
	c := initialized(t, ts)

	var created protocol.SessionCreateResult
	c.mustResult(c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{AgentID: "fake"}), &created)
	c.mustResult(c.call(protocol.MethodTaskSend, protocol.TaskSendParams{SessionID: created.SessionID, Prompt: "hello"}), nil)
	c.waitEvent(protocol.EventTaskDone)

	var hist protocol.SessionHistoryResult
	c.mustResult(c.call(protocol.MethodSessionHistory, protocol.SessionHistoryParams{SessionID: created.SessionID}), &hist)
	if len(hist.Events) < 3 {
		t.Fatalf("history too short: %+v", hist)
	}
	if hist.Events[1].Type != protocol.EventOutputText || hist.Events[1].Data["text"] != "echo:hello" {
		t.Fatalf("event[1] = %+v", hist.Events[1])
	}
	if hist.NextSeq != hist.Events[len(hist.Events)-1].Seq+1 {
		t.Fatalf("nextSeq = %d", hist.NextSeq)
	}
	// Paging: fromSeq = nextSeq returns empty, same cursor.
	var page2 protocol.SessionHistoryResult
	c.mustResult(c.call(protocol.MethodSessionHistory, protocol.SessionHistoryParams{SessionID: created.SessionID, FromSeq: hist.NextSeq}), &page2)
	if len(page2.Events) != 0 {
		t.Fatalf("page2 should be empty: %+v", page2.Events)
	}
	if resp := c.call(protocol.MethodSessionHistory, protocol.SessionHistoryParams{SessionID: "s_nope"}); resp.Error == nil {
		t.Fatal("unknown session should error")
	}
}
