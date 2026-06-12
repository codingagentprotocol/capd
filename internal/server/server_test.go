package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
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
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") || !strings.Contains(got, "connect-src 'self' http://127.0.0.1:* http://localhost:* http://[::1]:* ws://127.0.0.1:* ws://localhost:* ws://[::1]:*") || strings.Contains(got, " ws:;") || strings.Contains(got, " http:;") || !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("csp = %q", got)
	}
	if got := rec.Header().Get("Permissions-Policy"); got != "camera=(), microphone=(), geolocation=()" {
		t.Fatalf("permissions policy = %q", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("referrer policy = %q", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("x-frame-options = %q", got)
	}
	if !strings.Contains(rec.Body.String(), "accounts/list") {
		t.Fatal("console HTML missing accounts/list integration")
	}
	if !strings.Contains(rec.Body.String(), "accounts/quota") {
		t.Fatal("console HTML missing accounts/quota integration")
	}
	if !strings.Contains(rec.Body.String(), "agents/route") {
		t.Fatal("console HTML missing agents/route integration")
	}
	if !strings.Contains(rec.Body.String(), "session/attach") {
		t.Fatal("console HTML missing session attach integration")
	}
}

func TestProbeServedWithSecurityHeaders(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/probe/", nil)
	rec := httptest.NewRecorder()
	s.handleProbe(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache = %q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") || !strings.Contains(got, "connect-src 'self' http://127.0.0.1:* http://localhost:* http://[::1]:* ws://127.0.0.1:* ws://localhost:* ws://[::1]:*") || !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("csp = %q", got)
	}
	body := rec.Body.String()
	for _, want := range []string{"CAPD Probe", "accounts/check", "agents/route", "/healthz", "requireMultiple", "requireAllFreshQuota", "Evidence JSON", "Validation Tests", "validationRows", "showTests", "daemon health", "multi-account readiness", "quota freshness", "auto route fresh", "native secret backend", "readiness gate", "readinessError", "routeError", "routeDecision", "routeDecisionText", "decision.reason", `call("accounts/check", { provider:"codex" })`} {
		if !strings.Contains(body, want) {
			t.Fatalf("probe HTML missing %q", want)
		}
	}
	for _, forbidden := range []string{"secretRef", "secret_ref", "rawAuthJson", "RawAuthJSON", "localStorage.setItem", "sessionStorage.setItem", "?token=${TOKEN}"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("probe HTML contains forbidden %q", forbidden)
		}
	}
}

func TestConsoleStaticContract(t *testing.T) {
	html := consoleHTML
	required := []string{
		`value="auto"`,
		"自动账号",
		"accounts/list",
		`call("accounts/list", {})`,
		`call("accounts/list", { provider: "codex" })`,
		"accounts/import",
		"accounts/current",
		"accounts/project",
		"accounts/check",
		"accounts/quota",
		"accounts/remove",
		"agents/route",
		"accountRoute",
		"previewRoute",
		"routePreview",
		"routeDecisionSummary",
		"route ${routeDecisionSummary(route)}",
		"routeError",
		"route error ${routeError}",
		"readiness",
		"refreshDiagnostic",
		"刷新诊断",
		"refreshReadinessDiagnostic",
		"checkParams.requireSecretBackend = \"native\"",
		"诊断等待连接",
		"诊断中",
		"诊断不可用",
		"daemon ok",
		"quota fresh",
		"accountEvidence",
		"accounts ${accountEvidence}",
		"auto route 缺 fresh quota",
		"routeEvidenceSummary",
		"routeEvidenceSummary(result.autoRoute)",
		"route.primaryUsedPercent",
		"route.checkedAt",
		"codexAccounts",
		"暂无账号，先导入 Codex",
		"暂无 Codex 账号，先导入 Codex",
		"refreshSelectedQuota",
		"refreshAllQuota",
		"刷新全部 quota",
		"quota 批量刷新完成",
		"quota 批量刷新失败",
		`call("accounts/quota", { provider: "codex", accountId: "all" })`,
		"importCodexAccount",
		"导入 Codex",
		"多个路径用逗号分隔",
		"params.authPaths = authPaths",
		"result.accounts",
		"checkAccounts",
		"检查账号",
		"checkReady",
		"就绪门禁",
		"就绪门禁失败",
		`readinessGate ? "就绪门禁失败" : "账号检查失败"`,
		"readiness quota refreshed",
		"quota evidence",
		"accountCheckSummary",
		"a.quotaFresh",
		"a.primaryUsedPercent",
		"a.quotaCheckedAt",
		"not-fresh",
		"result.quotaRefreshed",
		"params.refreshQuota = true",
		"params.requireMultiple = true",
		"params.requireFreshQuota = true",
		"params.requireAllFreshQuota = true",
		`params.requireSecretBackend = "native"`,
		"requireNativeSecret",
		"native secret",
		"setCurrentAccount",
		"设为当前",
		"projectAccount",
		"准备运行",
		"removeAccount",
		"移除",
		"requireFreshQuota",
		`id="requireFreshQuota"`,
		"dataset.accountId",
		`params.accountId = accountId`,
		`params.requireFreshQuota = true`,
		"session/list",
		"session/attach",
		"session/create",
		`create.requireFreshQuota = true`,
		"res.accountId",
		"account ${res.accountId}",
		"发送失败",
		`a.provider === "codex"`,
		`provider.textContent = a.provider`,
		"QUOTA_FRESH_MS",
		"quotaState(a.quota)",
		"q.quotaState",
		`return "missing"`,
		"quotaFresh(q)",
		`? "fresh" : "stale"`,
		"hasNumber(a.quota.primaryUsedPercent)",
		"s.accountId",
		"button.disabled = true",
		"rejectAllPending",
		"CAP WebSocket is not connected",
		"CAP WebSocket closed",
		"CAP WebSocket error",
		"CAP WebSocket connect timeout",
		"ws.readyState !== WebSocket.OPEN",
		"delete pending[id]",
		"ws.onclose",
		"ws.onerror",
		`searchParams.delete("token")`,
		`searchParams.set("token", TOKEN)`,
		"capdWebSocketURL",
		"safeCAPDHost",
		"params.get(\"capd\")",
		`host === "localhost"`,
		`host === "127.0.0.1"`,
		`host === "[::1]"`,
		`location.host || "127.0.0.1:7777"`,
		"history.replaceState",
		"capdHTTPURL",
		"checkDaemonHealth",
		"checkDaemonHealth(false)",
		`fetch(capdHTTPURL("/healthz")`,
		"health ok",
	}
	for _, needle := range required {
		if !strings.Contains(html, needle) {
			t.Fatalf("console HTML missing %q", needle)
		}
	}
	forbidden := []string{
		"secretRef",
		"secret_ref",
		"rawAuthJson",
		"RawAuthJSON",
		"localStorage.setItem",
		"sessionStorage.setItem",
		"?token=${TOKEN}",
	}
	for _, needle := range forbidden {
		if strings.Contains(html, needle) {
			t.Fatalf("console HTML contains forbidden token %q", needle)
		}
	}
}

func TestConsoleExampleMatchesEmbedded(t *testing.T) {
	data, err := os.ReadFile("../../examples/web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != consoleHTML {
		t.Fatal("examples/web/index.html differs from embedded console_index.html")
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
