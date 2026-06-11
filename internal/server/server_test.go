package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/session"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	reg := adapter.NewRegistry()
	s := New(Options{
		Token:    "test-token",
		Version:  "test",
		Registry: reg,
		Sessions: session.NewManager(reg, nil),
		Log:      slog.New(slog.NewTextHandler(testWriter{t}, nil)),
	})
	ts := httptest.NewServer(http.HandlerFunc(s.handleWS))
	t.Cleanup(ts.Close)
	return s, ts
}

func TestWSRejectsMissingToken(t *testing.T) {
	_, ts := newTestServer(t)
	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestConsoleServedWithSecurityHeaders(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/console/", nil)
	rec := httptest.NewRecorder()
	s.handleConsole(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache = %q", got)
	}
	if !strings.Contains(rec.Body.String(), "accounts/list") {
		t.Fatal("console HTML missing accounts/list integration")
	}
	if !strings.Contains(rec.Body.String(), "session/attach") {
		t.Fatal("console HTML missing session attach integration")
	}
}

func TestInitializeHandshake(t *testing.T) {
	_, ts := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "?token=test-token"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	id := json.RawMessage(`1`)
	params, _ := json.Marshal(protocol.InitializeParams{
		ProtocolVersion: protocol.Version,
		Client:          protocol.ClientInfo{Name: "test"},
	})
	req, _ := json.Marshal(protocol.Request{
		JSONRPC: protocol.JSONRPCVersion, ID: &id,
		Method: protocol.MethodInitialize, Params: params,
	})
	if err := conn.Write(ctx, websocket.MessageText, req); err != nil {
		t.Fatal(err)
	}

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var resp protocol.Response
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	var result protocol.InitializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.ProtocolVersion != protocol.Version || result.Daemon.Name != "capd" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}
