package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/codingagentprotocol/capd/internal/config"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestDaemonWSURLEncodesToken(t *testing.T) {
	raw := daemonWSURL(config.Config{Host: "127.0.0.1", Port: 7777}, "tok &with?chars")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "ws" || u.Host != "127.0.0.1:7777" || u.Path != "/ws" {
		t.Fatalf("url = %q", raw)
	}
	if got := u.Query().Get("token"); got != "tok &with?chars" {
		t.Fatalf("token = %q", got)
	}
	if u.RawQuery == "token=tok &with?chars" {
		t.Fatalf("token was not escaped: %q", raw)
	}
}

func TestDaemonWSURLHandlesIPv6Host(t *testing.T) {
	raw := daemonWSURL(config.Config{Host: "::1", Port: 7777}, "tok")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "[::1]:7777" || u.Query().Get("token") != "tok" {
		t.Fatalf("url = %q", raw)
	}
}

func TestConsoleURLEncodesToken(t *testing.T) {
	raw := consoleURL(config.Config{Host: "localhost", Port: 17777}, "tok+with&chars")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "http" || u.Host != "localhost:17777" || u.Path != "/console/" {
		t.Fatalf("url = %q", raw)
	}
	if got := u.Query().Get("token"); got != "tok+with&chars" {
		t.Fatalf("token = %q", got)
	}
}

func TestDaemonAddrOmitsToken(t *testing.T) {
	cfg := config.Config{Host: "127.0.0.1", Port: 7777}
	if got := daemonAddr(cfg); got != "127.0.0.1:7777" {
		t.Fatalf("addr = %q", got)
	}
	if strings.Contains(daemonAddr(cfg), "token") {
		t.Fatalf("display address contains token material")
	}
}

func TestRunTaskConnectErrorDoesNotLeakToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", "1")
	token := "tok &with?chars"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := newRunCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	err := runTask(cmd, runOpts{agent: "codex", prompt: "hello"})
	if err == nil {
		t.Fatal("expected connect error")
	}
	text := err.Error()
	if strings.Contains(text, token) || strings.Contains(text, url.QueryEscape(token)) {
		t.Fatalf("connect error leaked token: %s", text)
	}
	if !strings.Contains(text, "127.0.0.1:1") {
		t.Fatalf("connect error missing display address: %s", text)
	}
}

func TestRunTaskSendsRequireFreshQuotaForAutoAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-run-fresh"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	seenCreate := make(chan protocol.SessionCreateParams, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("token"); got != token {
			t.Errorf("token = %q", got)
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var req struct {
				ID     int             `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(data, &req); err != nil {
				t.Errorf("request json: %v", err)
				return
			}
			switch req.Method {
			case protocol.MethodInitialize:
				writeWSJSON(t, r.Context(), conn, map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result":  protocol.InitializeResult{ProtocolVersion: protocol.Version},
				})
			case protocol.MethodSessionCreate:
				var params protocol.SessionCreateParams
				if err := json.Unmarshal(req.Params, &params); err != nil {
					t.Errorf("session/create params: %v", err)
					return
				}
				seenCreate <- params
				writeWSJSON(t, r.Context(), conn, map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result":  protocol.SessionCreateResult{SessionID: "s_test"},
				})
			case protocol.MethodTaskSend:
				writeWSJSON(t, r.Context(), conn, map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result":  map[string]any{},
				})
				writeWSJSON(t, r.Context(), conn, map[string]any{
					"jsonrpc": "2.0",
					"method":  protocol.MethodEvent,
					"params": protocol.Event{
						SessionID: "s_test",
						Seq:       1,
						Type:      protocol.EventTaskDone,
						Data:      map[string]any{"ok": true},
					},
				})
				return
			default:
				t.Errorf("unexpected method %q", req.Method)
				return
			}
		}
	}))
	defer ts.Close()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := newRunCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	if err := runTask(cmd, runOpts{
		agent:             "codex",
		account:           protocol.AccountAuto,
		requireFreshQuota: true,
		prompt:            "hello",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case params := <-seenCreate:
		if params.AgentID != "codex" || params.AccountID != protocol.AccountAuto || !params.RequireFreshQuota {
			t.Fatalf("session/create params = %+v", params)
		}
	case <-ctx.Done():
		t.Fatal("did not receive session/create")
	}
}

func writeWSJSON(t *testing.T, ctx context.Context, conn *websocket.Conn, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
}

func writeTokenForTest(home, token string) error {
	dir := filepath.Join(home, ".capd")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "token"), []byte(token+"\n"), 0o600)
}
