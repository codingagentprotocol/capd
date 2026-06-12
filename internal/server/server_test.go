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

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/secret"
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

func TestHealthzJSONReportsSafeDaemonMetadata(t *testing.T) {
	reg := adapter.NewRegistry()
	secretRoot := t.TempDir()
	s := New(Options{
		Token:    "test-token",
		Version:  "test-version",
		Registry: reg,
		Sessions: session.NewManager(reg, nil),
		Secrets:  secret.NewFileStore(secretRoot),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz?format=json", nil)
	rec := httptest.NewRecorder()
	s.handleHealthz(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type = %q", got)
	}
	var got struct {
		OK              bool   `json:"ok"`
		Daemon          string `json:"daemon"`
		Version         string `json:"version"`
		ProtocolVersion string `json:"protocolVersion"`
		SecretBackend   string `json:"secretBackend"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Daemon != "capd" || got.Version != "test-version" || got.ProtocolVersion != protocol.Version || got.SecretBackend != secret.BackendFile {
		t.Fatalf("health json = %+v", got)
	}
	for _, leaked := range []string{"test-token", secretRoot, "secretRef", "rawAuthJson"} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Fatalf("health json leaked %q: %s", leaked, rec.Body.String())
		}
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
	for _, want := range []string{"CAPD Probe", "accounts/check", "accounts/quota", "agents/usage", "agents/route", "/healthz?format=json", "/probe/data", "Authorization:`Bearer ${TOKEN}`", "fetchProbeData", "httpProbe", "fetchHealthInfo", "healthEvidence", "health: healthInfo", "protocolVersion", "secretBackend", "requireMultiple", "requireAllFreshQuota", "Evidence JSON", "Validation Tests", "Next step", "nextStep", "checks", "validationRows", "showTests", "daemon health", "multi-account readiness", "quota freshness", "auto route fresh", "native secret backend", "readiness gate", "rpcError", "e.data", "capd accounts check --readiness", "capd agents route --account auto", "CAPD_SECRET_BACKEND=native capd start", "deep verify with: capd doctor --json --fail --verify-secretstore --require-secret-backend native", "nativeSecretNextStep", "readinessError", "routeError", "routeDecision", "routeDecisionText", "routeCandidates", "route candidates", "routeCandidateText", "decision.reason", "RPC_TIMEOUT_MS = 12000", "LONG_RPC_TIMEOUT_MS = 120000", "rpcTimeoutFor", `method === "accounts/check" && (params.refreshQuota || params.requireFreshQuota || params.requireAllFreshQuota || params.requireMultiple)`, `timeout after ${timeoutMS}ms`, "clearTimeout(pending.timer)", `call("accounts/check", { provider:"codex" })`} {
		if !strings.Contains(body, want) {
			t.Fatalf("probe HTML missing %q", want)
		}
	}
	for _, forbidden := range []string{"secretRef", "secret_ref", "rawAuthJson", "RawAuthJSON", "localStorage.setItem", "sessionStorage.setItem", "?token=${TOKEN}"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("probe HTML contains forbidden %q", forbidden)
		}
	}
	for _, want := range []string{"webSocketAuthProtocol", "new WebSocket(wsURL(), [webSocketAuthProtocol(TOKEN)])"} {
		if !strings.Contains(body, want) {
			t.Fatalf("probe HTML missing subprotocol auth %q", want)
		}
	}
}

func TestProbeDataRequiresAuthorizationHeader(t *testing.T) {
	s, _ := newTestServer(t)
	for _, target := range []string{"/probe/data", "/probe/data?token=test-token"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		s.handleProbeData(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d", target, rec.Code)
		}
	}
}

func TestProbeDataTimeoutBudgetMatchesClientReadinessWindow(t *testing.T) {
	if got := probeDataTimeout(false); got != 12*time.Second {
		t.Fatalf("default probe timeout = %s", got)
	}
	if got := probeDataTimeout(true); got != 2*time.Minute {
		t.Fatalf("readiness probe timeout = %s", got)
	}
}

func TestProbeDataReturnsSafeAccountRouteEvidence(t *testing.T) {
	s, _, _, accounts := newCodexAccountIntegrationServer(t)
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 12}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/probe/data", nil)
	req.Header.Set("Authorization", "Bearer it-token")
	rec := httptest.NewRecorder()
	s.handleProbeData(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got probeDataResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK {
		t.Fatalf("probe data not ok: %+v body=%s", got.Errors, rec.Body.String())
	}
	if got.AccountsCheck == nil || got.AccountsCheck.CheckedAccounts != 1 {
		t.Fatalf("accountsCheck = %+v", got.AccountsCheck)
	}
	if got.RouteDecision == nil || got.RouteDecision.AccountID != "codex-test" {
		t.Fatalf("routeDecision = %+v", got.RouteDecision)
	}
	if got.AutoRoute == nil || got.AutoRoute.AccountID != "codex-test" || !got.AutoRoute.Fresh {
		t.Fatalf("autoRoute = %+v", got.AutoRoute)
	}
	if !got.Summary.Ready || got.Summary.Readiness || got.Summary.CheckedAccounts != 1 || got.Summary.RequiredAccounts != 2 || got.Summary.MissingAccounts != 1 || got.Summary.FreshQuotaAccounts != 1 || got.Summary.AutoRouteAccountID != "codex-test" || !got.Summary.AutoRouteFresh || !got.Summary.RouteDecisionOK || got.Summary.RouteCandidates != 1 || !got.Summary.SecretBackendOK {
		t.Fatalf("summary = %+v", got.Summary)
	}
	body := rec.Body.String()
	for _, leaked := range []string{"it-token", "test-token", "secretRef", "rawAuthJson", s.opts.RuntimeRoot} {
		if strings.Contains(body, leaked) {
			t.Fatalf("probe data leaked %q: %s", leaked, body)
		}
	}
}

func TestProbeDataReadinessReturnsPartialEvidenceOnFailure(t *testing.T) {
	s, _, _, accounts := newCodexAccountIntegrationServer(t)
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 12}); err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"planType": "pro",
			"rateLimits": map[string]any{
				"primary": map[string]any{"usedPercent": 12},
			},
		})
	}))
	defer backend.Close()
	s.opts.CodexQuotaBaseURL = backend.URL
	req := httptest.NewRequest(http.MethodGet, "/probe/data?readiness=1&requireSecretBackend=file", nil)
	req.Header.Set("Authorization", "Bearer it-token")
	rec := httptest.NewRecorder()
	s.handleProbeData(rec, req)
	if rec.Code != http.StatusFailedDependency {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got probeDataResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK {
		t.Fatal("readiness unexpectedly ok with one account")
	}
	if got.AccountsCheck == nil || got.AccountsCheck.CheckedAccounts != 1 {
		t.Fatalf("partial accountsCheck = %+v", got.AccountsCheck)
	}
	if len(got.AccountsCheck.Accounts) != 1 || !got.AccountsCheck.Accounts[0].QuotaFresh || !got.AccountsCheck.Accounts[0].RuntimeReady {
		t.Fatalf("partial account evidence = %+v", got.AccountsCheck.Accounts)
	}
	if got.Summary.Ready || !got.Summary.Readiness || got.Summary.CheckedAccounts != 1 || got.Summary.MissingAccounts != 1 || got.Summary.FreshQuotaAccounts != 1 || got.Summary.RequiredSecretBackend != "file" || got.Summary.SecretBackend != "file" || !got.Summary.SecretBackendOK {
		t.Fatalf("partial summary = %+v", got.Summary)
	}
	if len(got.Errors) == 0 || !strings.Contains(got.Errors[0].Message, "expected multiple Codex accounts") {
		t.Fatalf("errors = %+v", got.Errors)
	}
	if !strings.Contains(rec.Body.String(), "multi-account readiness") {
		t.Fatalf("body missing readiness checks: %s", rec.Body.String())
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
		"routeCandidateSummary",
		"routeCandidates",
		"route candidates",
		"routeError",
		"route error ${routeError}",
		"readiness",
		"readinessChecks",
		"readinessCheckRows",
		"renderReadinessChecks",
		"Codex multi-account import",
		"Codex quota freshness",
		"Codex auto route freshness",
		"SecretStore backend",
		"导入至少两个账号",
		"capd accounts check --readiness",
		"capd agents route --account auto",
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
		"result.importedAccounts",
		"accountsImportNextStep",
		"继续导入第二个 Codex 账号",
		"运行就绪门禁或 capd accounts check --readiness",
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
		"rpcError",
		"error.data",
		"e.data",
		"partial evidence",
		"renderReadinessDiagnosticResult",
		"readinessError",
		"readiness gate",
		"requireNativeSecret",
		"native SecretStore",
		"deep verify with: capd doctor --json --fail --verify-secretstore --require-secret-backend native",
		"CAPD_SECRET_BACKEND=native capd start",
		"CAP WebSocket is not connected",
		"CAP WebSocket closed",
		"CAP WebSocket error",
		"CAP WebSocket connect timeout",
		"RPC_TIMEOUT_MS = 12000",
		"LONG_RPC_TIMEOUT_MS = 120000",
		"rpcTimeoutFor",
		`method === "accounts/quota"`,
		`method === "agents/usage"`,
		`method === "accounts/check" && (params.refreshQuota || params.requireFreshQuota || params.requireAllFreshQuota || params.requireMultiple)`,
		`timeout after ${timeoutMS}ms`,
		"clearTimeout(p.timer)",
		"ws.readyState !== WebSocket.OPEN",
		"delete pending[id]",
		"ws.onclose",
		"ws.onerror",
		`searchParams.delete("token")`,
		"capdWebSocketURL",
		"webSocketAuthProtocol",
		`new WebSocket(capdWebSocketURL(), [webSocketAuthProtocol(TOKEN)])`,
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
		`fetch(capdHTTPURL("/healthz?format=json")`,
		"health.protocolVersion",
		"health.secretBackend",
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
		`searchParams.set("token", TOKEN)`,
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

func TestInitializeHandshakeWithSubprotocolToken(t *testing.T) {
	_, ts := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{webSocketAuthSubprotocol("test-token")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if got := conn.Subprotocol(); got != webSocketAuthSubprotocol("test-token") {
		t.Fatalf("subprotocol = %q", got)
	}

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
		t.Fatalf("initialize error: %+v", resp.Error)
	}
}

func TestInitializeHandshakeWithAuthorizationHeader(t *testing.T) {
	_, ts := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1)
	h := http.Header{}
	h.Set("Authorization", "Bearer test-token")
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: h})
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
		t.Fatalf("initialize error: %+v", resp.Error)
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
