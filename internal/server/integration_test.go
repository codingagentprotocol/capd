package server

// Protocol-level integration suite: a real WS server and a scripted fake
// adapter exercise every method and error path without touching a real
// agent CLI. Live-agent smoke tests stay outside CI (see docs/testing.md).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/session"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// ---- scripted fake adapter ----

type scriptedAdapter struct {
	mu        sync.Mutex
	id        string
	sessions  []*scriptedSession
	usageEnv  []string
	usageHook func()
}

func (f *scriptedAdapter) ID() string {
	if f.id != "" {
		return f.id
	}
	return "fake"
}
func (f *scriptedAdapter) Probe(context.Context) (protocol.AgentInfo, error) {
	return protocol.AgentInfo{ID: f.ID(), Name: "Fake", Available: true}, nil
}
func (f *scriptedAdapter) Capabilities() protocol.AgentCapabilities {
	return protocol.AgentCapabilities{
		Model:     true,
		Effort:    true,
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

func (f *scriptedAdapter) Usage(context.Context) (map[string]any, error) {
	return map[string]any{"planType": "default"}, nil
}

func (f *scriptedAdapter) UsageFor(_ context.Context, opts adapter.SessionOpts) (map[string]any, error) {
	f.mu.Lock()
	f.usageEnv = append([]string(nil), opts.Env...)
	hook := f.usageHook
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return map[string]any{
		"planType": "pro",
		"rateLimits": map[string]any{
			"primary": map[string]any{"usedPercent": 25.0, "resetsAt": "2026-06-11T20:00:00Z"},
		},
	}, nil
}

func (f *scriptedAdapter) lastOpts() adapter.SessionOpts {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sessions) == 0 {
		return adapter.SessionOpts{}
	}
	return f.sessions[len(f.sessions)-1].opts
}

func (f *scriptedAdapter) lastUsageEnv() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.usageEnv...)
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
	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "?token=" + url.QueryEscape(token)
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
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
	return newIntegrationWithToken(t, "it-token")
}

func newIntegrationWithToken(t *testing.T, token string) (*httptest.Server, *scriptedAdapter) {
	t.Helper()
	fake := &scriptedAdapter{}
	reg := adapter.NewRegistry(fake)
	st, err := session.OpenStore(t.TempDir() + "/it.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(Options{
		Token: token, Version: "it",
		Registry: reg, Sessions: session.NewManager(reg, st),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(http.HandlerFunc(s.handleWS))
	t.Cleanup(ts.Close)
	return ts, fake
}

func newCodexAccountIntegration(t *testing.T) (*httptest.Server, *scriptedAdapter, *account.Store) {
	_, ts, fake, accounts := newCodexAccountIntegrationServer(t)
	return ts, fake, accounts
}

func newCodexAccountIntegrationServer(t *testing.T) (*Server, *httptest.Server, *scriptedAdapter, *account.Store) {
	t.Helper()
	fake := &scriptedAdapter{id: "codex"}
	reg := adapter.NewRegistry(fake)
	dir := t.TempDir()
	st, err := session.OpenStore(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	accounts, err := account.OpenStore(filepath.Join(dir, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { accounts.Close() })
	secrets := secret.NewFileStore(filepath.Join(dir, "secrets"))
	ref, err := secrets.Put(context.Background(), "codex-test", secret.Bundle{
		Provider:    codexauth.Provider,
		AuthMode:    "oauth",
		AccessToken: "test-token",
		AccountID:   "acct_test",
		Email:       "codex@example.com",
		RawAuthJSON: []byte(`{"tokens":{"access_token":"test-token"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.UpsertAccount(account.Account{
		ID:        "codex-test",
		Provider:  codexauth.Provider,
		AuthMode:  "oauth",
		Email:     "codex@example.com",
		AccountID: "acct_test",
		SecretRef: ref.String(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SetCurrentAccount(codexauth.Provider, "codex-test"); err != nil {
		t.Fatal(err)
	}

	s := New(Options{
		Token: "it-token", Version: "it",
		Registry: reg, Sessions: session.NewManager(reg, st),
		Accounts: accounts, Secrets: secrets, RuntimeRoot: filepath.Join(dir, "runtimes"),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(http.HandlerFunc(s.handleWS))
	t.Cleanup(ts.Close)
	return s, ts, fake, accounts
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

func TestWebSocketTokenAllowsURLSpecialChars(t *testing.T) {
	token := "tok+with&chars?and space"
	ts, _ := newIntegrationWithToken(t, token)
	c := dialClient(t, ts, token)
	resp := c.call(protocol.MethodInitialize, protocol.InitializeParams{ProtocolVersion: protocol.Version})
	c.mustResult(resp, nil)
}

func TestAcceptsIPv6LoopbackOrigin(t *testing.T) {
	ts, _ := newIntegration(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "?token=it-token"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://[::1]:3000"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
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
	if !got.Model || !got.Effort || !got.Streaming || !got.Approvals || !got.Steer || !got.Fork || !got.Rollback || !got.Review || !got.Images || !got.Usage || !got.Resume {
		t.Fatalf("capabilities = %+v", got)
	}
}

func TestAgentsRouteAndAutoCreate(t *testing.T) {
	ts, _ := newIntegration(t)
	c := initialized(t, ts)

	var routed protocol.AgentRouteResult
	c.mustResult(c.call(protocol.MethodAgentsRoute, protocol.AgentRouteParams{
		Effort:       "high",
		Capabilities: protocol.AgentCapabilities{Review: true},
	}), &routed)
	if routed.Agent.ID != "fake" || routed.Reason == "" {
		t.Fatalf("route = %+v", routed)
	}

	var created protocol.SessionCreateResult
	c.mustResult(c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{
		AgentID: protocol.AgentAuto,
		Effort:  "high",
	}), &created)
	var list protocol.SessionListResult
	c.mustResult(c.call(protocol.MethodSessionList, struct{}{}), &list)
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != created.SessionID || list.Sessions[0].AgentID != "fake" {
		t.Fatalf("sessions = %+v", list.Sessions)
	}
}

func TestSessionCreateWithCodexAccountProjectsRuntime(t *testing.T) {
	ts, fake, accounts := newCodexAccountIntegration(t)
	c := initialized(t, ts)

	var created protocol.SessionCreateResult
	c.mustResult(c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{
		AgentID:   "codex",
		AccountID: "codex-test",
	}), &created)

	opts := fake.lastOpts()
	if len(opts.Env) != 1 || !strings.HasPrefix(opts.Env[0], "CODEX_HOME=") {
		t.Fatalf("Env = %#v", opts.Env)
	}
	codexHome := strings.TrimPrefix(opts.Env[0], "CODEX_HOME=")
	if _, err := os.Stat(filepath.Join(codexHome, "auth.json")); err != nil {
		t.Fatal(err)
	}
	accountID, err := accounts.SessionAccount(created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if accountID != "codex-test" {
		t.Fatalf("session account = %q", accountID)
	}
	var list protocol.SessionListResult
	c.mustResult(c.call(protocol.MethodSessionList, struct{}{}), &list)
	if len(list.Sessions) != 1 || list.Sessions[0].AccountID != "codex-test" {
		t.Fatalf("sessions = %+v", list.Sessions)
	}
}

func TestAgentsUsageWithCodexAccountProjectsRuntimeAndCachesQuota(t *testing.T) {
	ts, fake, accounts := newCodexAccountIntegration(t)
	c := initialized(t, ts)

	var result protocol.AgentsUsageResult
	c.mustResult(c.call(protocol.MethodAgentsUsage, protocol.AgentsUsageParams{
		AgentID:   "codex",
		AccountID: "codex-test",
	}), &result)
	if result.Usage["planType"] != "pro" {
		t.Fatalf("usage = %+v", result.Usage)
	}
	env := fake.lastUsageEnv()
	if len(env) != 1 || !strings.HasPrefix(env[0], "CODEX_HOME=") {
		t.Fatalf("usage env = %#v", env)
	}
	q, err := accounts.LoadQuota("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	if q.Plan != "pro" || q.PrimaryUsedPercent != 25 {
		t.Fatalf("quota = %+v", q)
	}
}

func TestAgentsUsageReportsQuotaCacheSaveFailure(t *testing.T) {
	ts, fake, accounts := newCodexAccountIntegration(t)
	fake.usageHook = func() {
		_ = accounts.Close()
	}
	c := initialized(t, ts)

	resp := c.call(protocol.MethodAgentsUsage, protocol.AgentsUsageParams{
		AgentID:   "codex",
		AccountID: "codex-test",
	})
	if resp.Error == nil || resp.Error.Code != protocol.CodeInternalError || !strings.Contains(resp.Error.Message, "save usage quota") {
		t.Fatalf("response = %+v", resp)
	}
}

func TestAgentsUsageWithAutoAccountChoosesLowestCachedQuota(t *testing.T) {
	ts, _, accounts := newCodexAccountIntegration(t)
	addCodexAccountForTest(t, accounts, "codex-high", "high@example.com")
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", PrimaryUsedPercent: 10}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-high", PrimaryUsedPercent: 90}); err != nil {
		t.Fatal(err)
	}
	c := initialized(t, ts)

	var result protocol.AgentsUsageResult
	c.mustResult(c.call(protocol.MethodAgentsUsage, protocol.AgentsUsageParams{
		AgentID:   "codex",
		AccountID: protocol.AccountAuto,
	}), &result)
	if result.AccountID != "codex-test" {
		t.Fatalf("usage account = %q", result.AccountID)
	}
	q, err := accounts.LoadQuota("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	if q.Plan != "pro" || q.PrimaryUsedPercent != 25 {
		t.Fatalf("quota = %+v", q)
	}
}

func TestAgentsRouteWithAccountRequiresCodex(t *testing.T) {
	ts, _, _ := newCodexAccountIntegration(t)
	c := initialized(t, ts)

	var routed protocol.AgentRouteResult
	c.mustResult(c.call(protocol.MethodAgentsRoute, protocol.AgentRouteParams{
		AccountID: "codex-test",
	}), &routed)
	if routed.Agent.ID != "codex" || !strings.Contains(routed.Reason, "accountId") {
		t.Fatalf("route = %+v", routed)
	}
	if routed.AccountRoute == nil || routed.AccountRoute.QuotaState != protocol.AccountQuotaStateMissing || routed.AccountRoute.Fresh || routed.AccountRoute.PrimaryUsedPercent != nil {
		t.Fatalf("account route = %+v", routed.AccountRoute)
	}
}

func TestAgentsRouteAutoAccountChoosesLowestCachedQuota(t *testing.T) {
	ts, _, accounts := newCodexAccountIntegration(t)
	addCodexAccountForTest(t, accounts, "codex-low", "low@example.com")
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 90}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-low", Plan: "pro", PrimaryUsedPercent: 10}); err != nil {
		t.Fatal(err)
	}
	c := initialized(t, ts)

	var routed protocol.AgentRouteResult
	c.mustResult(c.call(protocol.MethodAgentsRoute, protocol.AgentRouteParams{
		AccountID: protocol.AccountAuto,
	}), &routed)
	if routed.Agent.ID != "codex" || routed.AccountID != "codex-low" {
		t.Fatalf("route = %+v", routed)
	}
	if routed.AccountRoute == nil || routed.AccountRoute.QuotaState != protocol.AccountQuotaStateFresh || !routed.AccountRoute.Fresh || routed.AccountRoute.PrimaryUsedPercent == nil || *routed.AccountRoute.PrimaryUsedPercent != 10 {
		t.Fatalf("account route = %+v", routed.AccountRoute)
	}
	if !strings.Contains(routed.Reason, "auto account codex-low") {
		t.Fatalf("reason = %q", routed.Reason)
	}
}

func TestAgentsRouteAutoAccountRequireFreshQuota(t *testing.T) {
	ts, _, accounts := newCodexAccountIntegration(t)
	c := initialized(t, ts)

	resp := c.call(protocol.MethodAgentsRoute, protocol.AgentRouteParams{
		AccountID:         protocol.AccountAuto,
		RequireFreshQuota: true,
	})
	if resp.Error == nil || resp.Error.Code != protocol.CodeInvalidParams || !strings.Contains(resp.Error.Message, "fresh cached quota") {
		t.Fatalf("response = %+v", resp)
	}

	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 7}); err != nil {
		t.Fatal(err)
	}
	var routed protocol.AgentRouteResult
	c.mustResult(c.call(protocol.MethodAgentsRoute, protocol.AgentRouteParams{
		AccountID:         protocol.AccountAuto,
		RequireFreshQuota: true,
	}), &routed)
	if routed.AccountID != "codex-test" || routed.AccountRoute == nil || !routed.AccountRoute.Fresh || routed.AccountRoute.PrimaryUsedPercent == nil || *routed.AccountRoute.PrimaryUsedPercent != 7 {
		t.Fatalf("route = %+v", routed)
	}
}

func TestAgentsRouteAutoAccountIgnoresStaleLowQuota(t *testing.T) {
	ts, _, accounts := newCodexAccountIntegration(t)
	addCodexAccountForTest(t, accounts, "codex-fresh", "fresh@example.com")
	staleAt := time.Now().Add(-account.QuotaRouteCacheTTL - time.Minute).Unix()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 1, CheckedAt: staleAt}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-fresh", Plan: "pro", PrimaryUsedPercent: 20}); err != nil {
		t.Fatal(err)
	}
	c := initialized(t, ts)

	var routed protocol.AgentRouteResult
	c.mustResult(c.call(protocol.MethodAgentsRoute, protocol.AgentRouteParams{
		AccountID: protocol.AccountAuto,
	}), &routed)
	if routed.Agent.ID != "codex" || routed.AccountID != "codex-fresh" {
		t.Fatalf("route = %+v", routed)
	}
	if routed.AccountRoute == nil || routed.AccountRoute.QuotaState != protocol.AccountQuotaStateFresh || routed.AccountRoute.PrimaryUsedPercent == nil || *routed.AccountRoute.PrimaryUsedPercent != 20 {
		t.Fatalf("account route = %+v", routed.AccountRoute)
	}
	if !strings.Contains(routed.Reason, "auto account codex-fresh primary 20%") {
		t.Fatalf("reason = %q", routed.Reason)
	}
}

func TestSessionCreateAutoAccountBindsLowestCachedQuota(t *testing.T) {
	ts, _, accounts := newCodexAccountIntegration(t)
	addCodexAccountForTest(t, accounts, "codex-high", "high@example.com")
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", PrimaryUsedPercent: 10}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-high", PrimaryUsedPercent: 90}); err != nil {
		t.Fatal(err)
	}
	c := initialized(t, ts)

	var created protocol.SessionCreateResult
	c.mustResult(c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{
		AgentID:   protocol.AgentAuto,
		AccountID: protocol.AccountAuto,
	}), &created)
	accountID, err := accounts.SessionAccount(created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if accountID != "codex-test" {
		t.Fatalf("session account = %q", accountID)
	}
}

func TestSessionCreateAutoAccountRequireFreshQuota(t *testing.T) {
	ts, _, accounts := newCodexAccountIntegration(t)
	c := initialized(t, ts)

	resp := c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{
		AgentID:           protocol.AgentAuto,
		AccountID:         protocol.AccountAuto,
		RequireFreshQuota: true,
	})
	if resp.Error == nil || resp.Error.Code != protocol.CodeInvalidParams || !strings.Contains(resp.Error.Message, "fresh cached quota") {
		t.Fatalf("response = %+v", resp)
	}

	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", PrimaryUsedPercent: 6}); err != nil {
		t.Fatal(err)
	}
	var created protocol.SessionCreateResult
	c.mustResult(c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{
		AgentID:           protocol.AgentAuto,
		AccountID:         protocol.AccountAuto,
		RequireFreshQuota: true,
	}), &created)
	accountID, err := accounts.SessionAccount(created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if accountID != "codex-test" {
		t.Fatalf("session account = %q", accountID)
	}
}

func TestSessionCreateCodexWithAutoAccount(t *testing.T) {
	ts, _, accounts := newCodexAccountIntegration(t)
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", PrimaryUsedPercent: 5}); err != nil {
		t.Fatal(err)
	}
	c := initialized(t, ts)

	var created protocol.SessionCreateResult
	c.mustResult(c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{
		AgentID:   "codex",
		AccountID: protocol.AccountAuto,
	}), &created)
	accountID, err := accounts.SessionAccount(created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if accountID != "codex-test" {
		t.Fatalf("session account = %q", accountID)
	}
}

func TestSessionCreateCodexWithAutoAccountRequireFreshQuota(t *testing.T) {
	ts, _, accounts := newCodexAccountIntegration(t)
	c := initialized(t, ts)

	resp := c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{
		AgentID:           "codex",
		AccountID:         protocol.AccountAuto,
		RequireFreshQuota: true,
	})
	if resp.Error == nil || resp.Error.Code != protocol.CodeInvalidParams || !strings.Contains(resp.Error.Message, "fresh cached quota") {
		t.Fatalf("response = %+v", resp)
	}

	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", PrimaryUsedPercent: 6}); err != nil {
		t.Fatal(err)
	}
	var created protocol.SessionCreateResult
	c.mustResult(c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{
		AgentID:           "codex",
		AccountID:         protocol.AccountAuto,
		RequireFreshQuota: true,
	}), &created)
	accountID, err := accounts.SessionAccount(created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if accountID != "codex-test" {
		t.Fatalf("session account = %q", accountID)
	}
}

func TestAccountsListReturnsMetadataAndQuotaOnly(t *testing.T) {
	ts, _, accounts := newCodexAccountIntegration(t)
	if err := accounts.SaveQuota(account.QuotaSnapshot{
		AccountID:          "codex-test",
		Plan:               "pro",
		PrimaryUsedPercent: 25,
		PrimaryResetAt:     "2026-06-11T20:00:00Z",
		RawJSON:            `{"token":"must-not-return"}`,
	}); err != nil {
		t.Fatal(err)
	}
	c := initialized(t, ts)

	var result protocol.AccountsListResult
	c.mustResult(c.call(protocol.MethodAccountsList, protocol.AccountsListParams{
		Provider: codexauth.Provider,
	}), &result)
	if result.CurrentAccountID != "codex-test" || len(result.Accounts) != 1 {
		t.Fatalf("accounts = %+v", result)
	}
	acc := result.Accounts[0]
	if acc.ID != "codex-test" || acc.Email != "codex@example.com" || acc.Quota == nil {
		t.Fatalf("account = %+v", acc)
	}
	if acc.Quota.PrimaryUsedPercent != 25 || acc.Quota.PrimaryResetAt == "" || acc.Quota.QuotaState != protocol.AccountQuotaStateFresh {
		t.Fatalf("quota = %+v", acc.Quota)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "test-token") || strings.Contains(string(data), "secret") || strings.Contains(string(data), "must-not-return") {
		t.Fatalf("accounts/list leaked sensitive data: %s", data)
	}
}

func TestAccountsListWithoutProviderReturnsAllProviders(t *testing.T) {
	ts, _, accounts := newCodexAccountIntegration(t)
	if err := accounts.UpsertAccount(account.Account{
		ID:        "gemini-test",
		Provider:  "gemini",
		AuthMode:  "oauth",
		Email:     "gemini@example.com",
		SecretRef: "file:gemini-test",
	}); err != nil {
		t.Fatal(err)
	}
	c := initialized(t, ts)

	var result protocol.AccountsListResult
	c.mustResult(c.call(protocol.MethodAccountsList, protocol.AccountsListParams{}), &result)
	if result.CurrentAccountID != "" {
		t.Fatalf("current account for all providers = %q", result.CurrentAccountID)
	}
	if len(result.Accounts) != 2 {
		t.Fatalf("accounts = %+v", result.Accounts)
	}
	seen := map[string]bool{}
	for _, acc := range result.Accounts {
		seen[acc.Provider] = true
	}
	if !seen[codexauth.Provider] || !seen["gemini"] {
		t.Fatalf("providers = %+v accounts=%+v", seen, result.Accounts)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "secretRef") {
		t.Fatalf("accounts/list leaked sensitive data: %s", data)
	}
}

func TestAccountsListJSONIncludesZeroQuota(t *testing.T) {
	ts, _, accounts := newCodexAccountIntegration(t)
	if err := accounts.SaveQuota(account.QuotaSnapshot{
		AccountID:          "codex-test",
		Plan:               "pro",
		PrimaryUsedPercent: 0,
		CheckedAt:          1781170000,
		RawJSON:            `{"token":"must-not-return"}`,
	}); err != nil {
		t.Fatal(err)
	}
	c := initialized(t, ts)

	var result protocol.AccountsListResult
	c.mustResult(c.call(protocol.MethodAccountsList, protocol.AccountsListParams{
		Provider: codexauth.Provider,
	}), &result)
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"primaryUsedPercent":0`) {
		t.Fatalf("zero quota missing from JSON: %s", text)
	}
	if !strings.Contains(text, `"checkedAt":1781170000`) {
		t.Fatalf("checkedAt missing from JSON: %s", text)
	}
	if !strings.Contains(text, `"quotaState":"stale"`) {
		t.Fatalf("quotaState missing from JSON: %s", text)
	}
	if strings.Contains(text, "test-token") || strings.Contains(text, "secret") || strings.Contains(text, "must-not-return") {
		t.Fatalf("accounts/list leaked sensitive data: %s", text)
	}
}

func TestAccountsImportCodexAuthJSONWithoutLeakingSecrets(t *testing.T) {
	s, ts, _, accounts := newCodexAccountIntegrationServer(t)
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{
		"auth_mode": "chatgpt",
		"email": "imported@example.com",
		"tokens": {
			"access_token": "import-access-secret",
			"refresh_token": "import-refresh-secret",
			"account_id": "acct_imported"
		}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := initialized(t, ts)

	var result protocol.AccountsImportResult
	c.mustResult(c.call(protocol.MethodAccountsImport, protocol.AccountsImportParams{
		Provider: codexauth.Provider,
		AuthPath: authPath,
	}), &result)
	if result.Account.ID != "codex-acct_imported" || result.Account.Email != "imported@example.com" || result.Account.AccountID != "acct_imported" {
		t.Fatalf("result = %+v", result)
	}
	if result.Account.AuthMode != "chatgpt" || result.Account.Quota != nil {
		t.Fatalf("account = %+v", result.Account)
	}
	if result.CurrentAccountID != "codex-test" {
		t.Fatalf("current account = %q", result.CurrentAccountID)
	}
	acc, err := accounts.LoadAccount(result.Account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if acc.SecretRef == "" || strings.Contains(acc.SecretRef, "import-access-secret") {
		t.Fatalf("secret ref = %q", acc.SecretRef)
	}
	ref, err := secret.ParseRef(acc.SecretRef)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := s.opts.Secrets.Get(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.AccessToken != "import-access-secret" || bundle.RefreshToken != "import-refresh-secret" {
		t.Fatalf("bundle = %+v", bundle)
	}
	data, _ := json.Marshal(result)
	for _, leaked := range []string{"import-access-secret", "import-refresh-secret", "secretRef", "secret_ref", authPath} {
		if strings.Contains(string(data), leaked) {
			t.Fatalf("accounts/import leaked %q: %s", leaked, data)
		}
	}
}

func TestAccountsImportRejectsUnsupportedProviderAndInvalidAuth(t *testing.T) {
	_, ts, _, _ := newCodexAccountIntegrationServer(t)
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"email":"dev@example.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := initialized(t, ts)

	resp := c.call(protocol.MethodAccountsImport, protocol.AccountsImportParams{
		Provider: "gemini",
		AuthPath: authPath,
	})
	if resp.Error == nil || resp.Error.Code != protocol.CodeInvalidParams || !strings.Contains(resp.Error.Message, "supported only for provider") {
		t.Fatalf("provider response = %+v", resp)
	}
	resp = c.call(protocol.MethodAccountsImport, protocol.AccountsImportParams{
		Provider: codexauth.Provider,
		AuthPath: authPath,
	})
	if resp.Error == nil || resp.Error.Code != protocol.CodeInvalidParams || !strings.Contains(resp.Error.Message, "supported token field") {
		t.Fatalf("invalid auth response = %+v", resp)
	}
	if strings.Contains(resp.Error.Message, authPath) {
		t.Fatalf("import error leaked auth path: %v", resp.Error)
	}
	missingPath := filepath.Join(dir, "missing-auth.json")
	resp = c.call(protocol.MethodAccountsImport, protocol.AccountsImportParams{
		Provider: codexauth.Provider,
		AuthPath: missingPath,
	})
	if resp.Error == nil || resp.Error.Code != protocol.CodeInvalidParams || !strings.Contains(resp.Error.Message, "read auth json failed") {
		t.Fatalf("missing auth response = %+v", resp)
	}
	if strings.Contains(resp.Error.Message, missingPath) {
		t.Fatalf("missing auth error leaked path: %v", resp.Error)
	}
}

func TestAccountsCurrentShowsAndSetsCurrentWithoutSecrets(t *testing.T) {
	ts, _, accounts := newCodexAccountIntegration(t)
	addCodexAccountForTest(t, accounts, "codex-alt", "alt@example.com")
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-alt", Plan: "pro", PrimaryUsedPercent: 7}); err != nil {
		t.Fatal(err)
	}
	c := initialized(t, ts)

	var before protocol.AccountsCurrentResult
	c.mustResult(c.call(protocol.MethodAccountsCurrent, protocol.AccountsCurrentParams{
		Provider: codexauth.Provider,
	}), &before)
	if before.CurrentAccountID != "codex-test" || before.Account == nil || before.Account.ID != "codex-test" {
		t.Fatalf("before = %+v", before)
	}

	var after protocol.AccountsCurrentResult
	c.mustResult(c.call(protocol.MethodAccountsCurrent, protocol.AccountsCurrentParams{
		Provider:  codexauth.Provider,
		AccountID: "codex-alt",
	}), &after)
	if after.CurrentAccountID != "codex-alt" || after.Account == nil || after.Account.Email != "alt@example.com" || after.Account.Quota == nil || after.Account.Quota.QuotaState != protocol.AccountQuotaStateFresh {
		t.Fatalf("after = %+v", after)
	}
	current, err := accounts.CurrentAccount(codexauth.Provider)
	if err != nil {
		t.Fatal(err)
	}
	if current != "codex-alt" {
		t.Fatalf("current = %q", current)
	}
	data, _ := json.Marshal(after)
	if strings.Contains(string(data), "test-token") || strings.Contains(string(data), "secret") || strings.Contains(string(data), "secretRef") {
		t.Fatalf("accounts/current leaked sensitive data: %s", data)
	}
}

func TestAccountsCurrentRejectsUnknownAndProviderMismatch(t *testing.T) {
	ts, _, accounts := newCodexAccountIntegration(t)
	if err := accounts.UpsertAccount(account.Account{
		ID:        "gemini-test",
		Provider:  "gemini",
		AuthMode:  "oauth",
		Email:     "gemini@example.com",
		SecretRef: "file:gemini-test",
	}); err != nil {
		t.Fatal(err)
	}
	c := initialized(t, ts)

	resp := c.call(protocol.MethodAccountsCurrent, protocol.AccountsCurrentParams{
		Provider:  codexauth.Provider,
		AccountID: "missing",
	})
	if resp.Error == nil || resp.Error.Code != protocol.CodeInvalidParams || !strings.Contains(resp.Error.Message, "unknown accountId") {
		t.Fatalf("missing response = %+v", resp)
	}
	resp = c.call(protocol.MethodAccountsCurrent, protocol.AccountsCurrentParams{
		Provider:  codexauth.Provider,
		AccountID: "gemini-test",
	})
	if resp.Error == nil || resp.Error.Code != protocol.CodeInvalidParams || !strings.Contains(resp.Error.Message, "not a codex account") {
		t.Fatalf("mismatch response = %+v", resp)
	}
}

func TestAccountsRemoveDeletesSecretProjectionAndMetadata(t *testing.T) {
	s, ts, _, accounts := newCodexAccountIntegrationServer(t)
	acc, err := accounts.LoadAccount("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := codexauth.RuntimeProjector{
		Root:    s.opts.RuntimeRoot,
		Secrets: s.opts.Secrets,
	}.Project(context.Background(), acc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(profile.CodexHome, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 22}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.BindSessionAccount("s_1", "codex-test"); err != nil {
		t.Fatal(err)
	}
	c := initialized(t, ts)

	var result protocol.AccountsRemoveResult
	c.mustResult(c.call(protocol.MethodAccountsRemove, protocol.AccountsRemoveParams{
		Provider:  codexauth.Provider,
		AccountID: "codex-test",
	}), &result)
	if result.AccountID != "codex-test" || !result.RuntimeRemoved || !result.CredentialRemoved || result.CurrentAccountID != "" || result.RemainingAccounts != 0 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(profile.CodexHome); !os.IsNotExist(err) {
		t.Fatalf("projection still exists err=%v", err)
	}
	if _, err := s.opts.Secrets.Get(context.Background(), secret.Ref{Backend: secret.BackendFile, ID: "codex-test"}); err == nil {
		t.Fatal("secret still readable after remove")
	}
	if _, err := accounts.LoadAccount("codex-test"); !strings.Contains(fmt.Sprint(err), "unknown account") {
		t.Fatalf("account err = %v", err)
	}
	if _, err := accounts.LoadQuota("codex-test"); !strings.Contains(fmt.Sprint(err), "unknown account") {
		t.Fatalf("quota err = %v", err)
	}
	sessionAccount, err := accounts.SessionAccount("s_1")
	if err != nil {
		t.Fatal(err)
	}
	if sessionAccount != "" {
		t.Fatalf("session account = %q", sessionAccount)
	}
	data, _ := json.Marshal(result)
	for _, leaked := range []string{"test-token", "secret", profile.CodexHome, "secretRef"} {
		if strings.Contains(string(data), leaked) {
			t.Fatalf("accounts/remove leaked %q: %s", leaked, data)
		}
	}
}

func TestAccountsRemoveRejectsUnsafeProjectionWithoutDeletingMetadata(t *testing.T) {
	s, ts, _, accounts := newCodexAccountIntegrationServer(t)
	unsafeHome := filepath.Join(s.opts.RuntimeRoot, codexauth.Provider, "codex-test")
	if err := os.MkdirAll(unsafeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unsafeHome, "auth.json"), []byte("test-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unsafeHome, ".capd_projection.json"), []byte(`{"managedBy":"other","provider":"codex","account":"codex-test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := initialized(t, ts)

	resp := c.call(protocol.MethodAccountsRemove, protocol.AccountsRemoveParams{
		Provider:  codexauth.Provider,
		AccountID: "codex-test",
	})
	if resp.Error == nil || resp.Error.Code != protocol.CodeInternalError || !strings.Contains(resp.Error.Message, "runtime projection marker mismatch") {
		t.Fatalf("response = %+v", resp)
	}
	if _, err := accounts.LoadAccount("codex-test"); err != nil {
		t.Fatalf("metadata should remain: %v", err)
	}
	if _, err := s.opts.Secrets.Get(context.Background(), secret.Ref{Backend: secret.BackendFile, ID: "codex-test"}); err != nil {
		t.Fatalf("secret should remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(unsafeHome, "auth.json")); err != nil {
		t.Fatalf("projection should remain: %v", err)
	}
	if strings.Contains(resp.Error.Message, "test-token") {
		t.Fatalf("remove error leaked token: %v", resp.Error)
	}
}

func addCodexAccountForTest(t *testing.T, accounts *account.Store, id, email string) {
	t.Helper()
	if err := accounts.UpsertAccount(account.Account{
		ID:        id,
		Provider:  codexauth.Provider,
		AuthMode:  "oauth",
		Email:     email,
		AccountID: "acct_" + id,
		SecretRef: "file:" + id,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAccountsQuotaRefreshesBackendQuotaSafely(t *testing.T) {
	s, ts, _, accounts := newCodexAccountIntegrationServer(t)
	var sawAuth, sawAccount, sawReferer string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		sawAuth = r.Header.Get("Authorization")
		sawAccount = r.Header.Get("ChatGPT-Account-Id")
		sawReferer = r.Header.Get("Referer")
		json.NewEncoder(w).Encode(map[string]any{
			"planType": "team",
			"rateLimits": map[string]any{
				"primary":    map[string]any{"usedPercent": 37, "resetsAt": "2026-06-11T20:00:00Z"},
				"secondary":  map[string]any{"usedPercent": 12, "resetsAt": "2026-06-11T21:00:00Z"},
				"codeReview": map[string]any{"usedPercent": 9},
			},
			"debug": "must-not-return",
		})
	}))
	defer backend.Close()
	s.opts.CodexQuotaBaseURL = backend.URL
	c := initialized(t, ts)

	var result protocol.AccountsQuotaResult
	c.mustResult(c.call(protocol.MethodAccountsQuota, protocol.AccountsQuotaParams{
		Provider:  codexauth.Provider,
		AccountID: "codex-test",
	}), &result)
	if sawAuth != "Bearer test-token" || sawAccount != "acct_test" || sawReferer != "https://chatgpt.com/" {
		t.Fatalf("headers auth=%q account=%q referer=%q", sawAuth, sawAccount, sawReferer)
	}
	if result.Account.ID != "codex-test" || result.Account.Email != "codex@example.com" || result.Account.Quota == nil {
		t.Fatalf("account = %+v", result.Account)
	}
	if result.Account.Quota.Plan != "team" || result.Account.Quota.PrimaryUsedPercent != 37 || result.Account.Quota.CodeReviewUsedPercent != 9 {
		t.Fatalf("quota = %+v", result.Account.Quota)
	}
	q, err := accounts.LoadQuota("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	if q.Plan != "team" || q.SecondaryUsedPercent != 12 {
		t.Fatalf("cached quota = %+v", q)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "test-token") || strings.Contains(string(data), "secret") || strings.Contains(string(data), "must-not-return") {
		t.Fatalf("accounts/quota leaked sensitive data: %s", data)
	}
}

func TestAccountsQuotaAutoRefreshesLowestCachedQuotaAccount(t *testing.T) {
	s, ts, _, accounts := newCodexAccountIntegrationServer(t)
	ref, err := s.opts.Secrets.Put(context.Background(), "codex-low", secret.Bundle{
		Provider:    codexauth.Provider,
		AuthMode:    "oauth",
		AccessToken: "low-token",
		AccountID:   "acct_low",
		Email:       "low@example.com",
		RawAuthJSON: []byte(`{"tokens":{"access_token":"low-token"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.UpsertAccount(account.Account{
		ID:        "codex-low",
		Provider:  codexauth.Provider,
		AuthMode:  "oauth",
		Email:     "low@example.com",
		AccountID: "acct_low",
		SecretRef: ref.String(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", PrimaryUsedPercent: 80}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-low", PrimaryUsedPercent: 5}); err != nil {
		t.Fatal(err)
	}
	var sawAuth, sawAccount string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawAccount = r.Header.Get("ChatGPT-Account-Id")
		json.NewEncoder(w).Encode(map[string]any{
			"planType": "team",
			"rateLimits": map[string]any{
				"primary": map[string]any{"usedPercent": 13},
			},
		})
	}))
	defer backend.Close()
	s.opts.CodexQuotaBaseURL = backend.URL
	c := initialized(t, ts)

	var result protocol.AccountsQuotaResult
	c.mustResult(c.call(protocol.MethodAccountsQuota, protocol.AccountsQuotaParams{
		Provider:  codexauth.Provider,
		AccountID: protocol.AccountAuto,
	}), &result)
	if result.Account.ID != "codex-low" || result.Account.Quota == nil || result.Account.Quota.PrimaryUsedPercent != 13 {
		t.Fatalf("account = %+v", result.Account)
	}
	if sawAuth != "Bearer low-token" || sawAccount != "acct_low" {
		t.Fatalf("headers auth=%q account=%q", sawAuth, sawAccount)
	}
	q, err := accounts.LoadQuota("codex-low")
	if err != nil {
		t.Fatal(err)
	}
	if q.Plan != "team" || q.PrimaryUsedPercent != 13 {
		t.Fatalf("cached quota = %+v", q)
	}
}

func TestAccountsQuotaRejectsMalformedSecretRef(t *testing.T) {
	_, ts, _, accounts := newCodexAccountIntegrationServer(t)
	if err := accounts.UpsertAccount(account.Account{
		ID:        "codex-test",
		Provider:  codexauth.Provider,
		AuthMode:  "oauth",
		Email:     "codex@example.com",
		AccountID: "acct_test",
		SecretRef: "file:",
	}); err != nil {
		t.Fatal(err)
	}
	c := initialized(t, ts)

	resp := c.call(protocol.MethodAccountsQuota, protocol.AccountsQuotaParams{
		Provider:  codexauth.Provider,
		AccountID: "codex-test",
	})
	if resp.Error == nil || resp.Error.Code != protocol.CodeInternalError || !strings.Contains(resp.Error.Message, "secret id is empty") {
		t.Fatalf("response = %+v", resp)
	}
}

func TestAccountsQuotaRejectsSecretBackendMismatchWithoutLeakingSecrets(t *testing.T) {
	_, ts, _, accounts := newCodexAccountIntegrationServer(t)
	if err := accounts.UpsertAccount(account.Account{
		ID:        "codex-test",
		Provider:  codexauth.Provider,
		AuthMode:  "oauth",
		Email:     "codex@example.com",
		AccountID: "acct_test",
		SecretRef: secret.BackendNative + ":codex-test",
	}); err != nil {
		t.Fatal(err)
	}
	c := initialized(t, ts)

	resp := c.call(protocol.MethodAccountsQuota, protocol.AccountsQuotaParams{
		Provider:  codexauth.Provider,
		AccountID: "codex-test",
	})
	if resp.Error == nil || resp.Error.Code != protocol.CodeInternalError || !strings.Contains(resp.Error.Message, `secret backend = "native", active backend = "file"`) {
		t.Fatalf("response = %+v", resp)
	}
	data, _ := json.Marshal(resp)
	for _, leaked := range []string{"test-token", "acct_test", "native:codex-test"} {
		if strings.Contains(string(data), leaked) {
			t.Fatalf("accounts/quota leaked %q: %s", leaked, data)
		}
	}
}

func TestSessionCreateRejectsAccountForNonCodexAgent(t *testing.T) {
	ts, _ := newIntegration(t)
	c := initialized(t, ts)
	resp := c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{
		AgentID:   "fake",
		AccountID: "codex-test",
	})
	if resp.Error == nil || resp.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("want invalid params, got %+v", resp)
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

func TestPolicyRejectsUnsafeInputs(t *testing.T) {
	ts, _ := newIntegration(t)
	c := initialized(t, ts)

	var created protocol.SessionCreateResult
	c.mustResult(c.call(protocol.MethodSessionCreate, protocol.SessionCreateParams{AgentID: "fake"}), &created)

	if resp := c.call(protocol.MethodTaskSend, protocol.TaskSendParams{SessionID: created.SessionID}); resp.Error == nil || resp.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("want empty task invalid params, got %+v", resp)
	}
	if resp := c.call(protocol.MethodTaskSend, protocol.TaskSendParams{
		SessionID: created.SessionID,
		Prompt:    "x",
		Attachments: []protocol.Attachment{{
			Type: "image",
			URL:  "file:///tmp/not-remote.png",
		}},
	}); resp.Error == nil || resp.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("want unsafe attachment invalid params, got %+v", resp)
	}
	if resp := c.call(protocol.MethodApprovalReply, protocol.ApprovalReplyParams{
		SessionID: created.SessionID, ApprovalID: "ap_1", Decision: "maybe",
	}); resp.Error == nil || resp.Error.Code != protocol.CodeInvalidParams {
		t.Fatalf("want invalid approval decision, got %+v", resp)
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

func TestReviewMultiCreatesReviewerSessions(t *testing.T) {
	ts, _ := newIntegration(t)
	c := initialized(t, ts)

	var result protocol.TaskReviewMultiResult
	c.mustResult(c.call(protocol.MethodTaskReviewMulti, protocol.TaskReviewMultiParams{
		Target: protocol.ReviewTarget{Type: "branch", Branch: "main"},
	}), &result)
	if len(result.Reviews) != 1 || result.Reviews[0].AgentID != "fake" || result.Reviews[0].SessionID == "" {
		t.Fatalf("review multi result = %+v", result)
	}
	if ev := c.waitEvent(protocol.EventOutputText); ev.SessionID != result.Reviews[0].SessionID || ev.Data["text"] != "review:branch" {
		t.Fatalf("review event = %+v", ev)
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
