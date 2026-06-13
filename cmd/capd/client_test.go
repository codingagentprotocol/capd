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

	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/config"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestDaemonWSURLOmitsToken(t *testing.T) {
	raw := daemonWSURL(config.Config{Host: "127.0.0.1", Port: 7777})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "ws" || u.Host != "127.0.0.1:7777" || u.Path != "/ws" {
		t.Fatalf("url = %q", raw)
	}
	if u.RawQuery != "" || strings.Contains(raw, "token") {
		t.Fatalf("url contains auth material: %q", raw)
	}
}

func TestDaemonWSURLHandlesIPv6Host(t *testing.T) {
	raw := daemonWSURL(config.Config{Host: "::1", Port: 7777})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "[::1]:7777" || u.RawQuery != "" {
		t.Fatalf("url = %q", raw)
	}
}

func TestDaemonDialOptionsUseAuthorizationHeader(t *testing.T) {
	opts := daemonDialOptions("tok &with?chars")
	if opts == nil {
		t.Fatal("nil options")
	}
	if got := opts.HTTPHeader.Get("Authorization"); got != "Bearer tok &with?chars" {
		t.Fatalf("Authorization = %q", got)
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

func TestConsoleURLAddsRequiredSecretBackend(t *testing.T) {
	raw := consoleURLWithSecretBackend(config.Config{Host: "localhost", Port: 17777}, "tok+with&chars", "native")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/console/" {
		t.Fatalf("url = %q", raw)
	}
	if got := u.Query().Get("token"); got != "tok+with&chars" {
		t.Fatalf("token = %q", got)
	}
	if got := u.Query().Get("requireSecretBackend"); got != "native" {
		t.Fatalf("requireSecretBackend = %q", got)
	}
}

func TestProbeURLEncodesToken(t *testing.T) {
	raw := probeURL(config.Config{Host: "localhost", Port: 17777}, "tok+with&chars")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "http" || u.Host != "localhost:17777" || u.Path != "/probe/" {
		t.Fatalf("url = %q", raw)
	}
	if got := u.Query().Get("token"); got != "tok+with&chars" {
		t.Fatalf("token = %q", got)
	}
}

func TestProbeURLAddsRequiredSecretBackend(t *testing.T) {
	raw := probeURLWithSecretBackend(config.Config{Host: "localhost", Port: 17777}, "tok+with&chars", "native")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/probe/" {
		t.Fatalf("url = %q", raw)
	}
	if got := u.Query().Get("token"); got != "tok+with&chars" {
		t.Fatalf("token = %q", got)
	}
	if got := u.Query().Get("requireSecretBackend"); got != "native" {
		t.Fatalf("requireSecretBackend = %q", got)
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

func TestRunTaskRequireFreshQuotaFailsFastWithoutAutoAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cmd := newRunCmd()
	cmd.SetOut(&bytes.Buffer{})
	err := runTask(cmd, runOpts{
		agent:             "codex",
		requireFreshQuota: true,
		prompt:            "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "--account auto") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunTaskRequireFreshQuotaFailsFastForExistingSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cmd := newRunCmd()
	cmd.SetOut(&bytes.Buffer{})
	err := runTask(cmd, runOpts{
		agent:             "codex",
		session:           "s_existing",
		account:           protocol.AccountAuto,
		requireFreshQuota: true,
		prompt:            "hello",
	})
	if err == nil || !strings.Contains(err.Error(), "only valid when creating a new session") {
		t.Fatalf("err = %v", err)
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
		if got := r.URL.RawQuery; got != "" {
			t.Errorf("query leaked auth material: %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
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
					"result":  protocol.SessionCreateResult{SessionID: "s_test", AccountID: "codex-low"},
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
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runTask(cmd, runOpts{
		agent:             " codex ",
		account:           " " + protocol.AccountAuto + " ",
		requireFreshQuota: true,
		prompt:            "hello",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "session s_test (codex · codex-low)") {
		t.Fatalf("output missing account evidence: %s", out.String())
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

func TestRunTaskFreshQuotaFailureSuggestsReadiness(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-run-fresh-fail"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.RawQuery; got != "" {
			t.Errorf("query leaked auth material: %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
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
				ID     int    `json:"id"`
				Method string `json:"method"`
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
				primary := 91.0
				writeWSJSON(t, r.Context(), conn, map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"error": &protocol.Error{
						Code:    protocol.CodeInvalidParams,
						Message: "auto route does not have fresh cached quota",
						Data: protocol.AgentRouteErrorData{
							SecretBackend: secret.BackendFile,
							AccountRoute: &protocol.AccountRouteEvidence{
								AccountID:          "codex-stale",
								QuotaState:         protocol.AccountQuotaStateStale,
								CheckedAt:          1700000000,
								PrimaryUsedPercent: &primary,
								Score:              75,
							},
							RouteCandidates: []protocol.AccountRouteEvidence{
								{
									AccountID:          "codex-stale",
									QuotaState:         protocol.AccountQuotaStateStale,
									CheckedAt:          1700000000,
									PrimaryUsedPercent: &primary,
									Score:              75,
								},
								{
									AccountID:  "codex-missing",
									QuotaState: protocol.AccountQuotaStateMissing,
									Score:      75,
								},
							},
						},
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
	t.Setenv(secret.EnvBackend, secret.BackendFile)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := newRunCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	err = runTask(cmd, runOpts{
		agent:             "codex",
		account:           protocol.AccountAuto,
		requireFreshQuota: true,
		prompt:            "hello",
	})
	if err == nil {
		t.Fatal("expected fresh quota error")
	}
	text := err.Error()
	for _, want := range []string{"fresh cached quota", "route: quota stale fresh false primary 91.0% score 75.00 checked", "route candidates: codex-stale quota stale", "codex-missing quota missing", "secret backend: file", "capd accounts check --json --readiness --require-secret-backend file --timeout 2m", "LIVE_SECRET_BACKEND=file make live-codex-preflight", "capd agents route --account auto --require-fresh-quota --json"} {
		if !strings.Contains(text, want) {
			t.Fatalf("error missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, token) {
		t.Fatalf("error leaked token: %s", text)
	}
}

func TestRunTaskFreshQuotaFailurePrefersRouteSecretBackend(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-run-fresh-native"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				ID     int    `json:"id"`
				Method string `json:"method"`
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
				writeWSJSON(t, r.Context(), conn, map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"error": &protocol.Error{
						Code:    protocol.CodeInvalidParams,
						Message: "auto route does not have fresh cached quota",
						Data: protocol.AgentRouteErrorData{
							SecretBackend: secret.BackendNative,
							AccountRoute: &protocol.AccountRouteEvidence{
								AccountID:  "codex-stale",
								QuotaState: protocol.AccountQuotaStateStale,
								Score:      75,
							},
						},
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
	t.Setenv(secret.EnvBackend, secret.BackendFile)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := newRunCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	err = runTask(cmd, runOpts{
		agent:             "codex",
		account:           protocol.AccountAuto,
		requireFreshQuota: true,
		prompt:            "hello",
	})
	if err == nil {
		t.Fatal("expected fresh quota error")
	}
	text := err.Error()
	want := "capd accounts check --json --readiness --require-secret-backend native --timeout 2m"
	if !strings.Contains(text, want) {
		t.Fatalf("error missing %q: %s", want, text)
	}
	if !strings.Contains(text, "LIVE_SECRET_BACKEND=native make live-codex-preflight") {
		t.Fatalf("error missing live preflight: %s", text)
	}
	if !strings.Contains(text, "secret backend: native") {
		t.Fatalf("error missing secret backend evidence: %s", text)
	}
	if strings.Contains(text, "--require-secret-backend file") {
		t.Fatalf("error used env backend instead of route backend: %s", text)
	}
	if strings.Contains(text, token) {
		t.Fatalf("error leaked token: %s", text)
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
