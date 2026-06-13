package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/security"
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
	s.clients.Add(2)
	s.metrics.recordAdapterStart(true)
	s.metrics.recordAdapterStart(false)
	s.metrics.recordRouteDecision("codex", true)
	s.metrics.recordRouteDecision("", false)

	req := httptest.NewRequest(http.MethodGet, "/healthz?format=json", nil)
	rec := httptest.NewRecorder()
	s.handleHealthz(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff = %q", got)
	}
	if got := rec.Header().Get("Permissions-Policy"); got != "camera=(), microphone=(), geolocation=()" {
		t.Fatalf("permissions policy = %q", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("referrer policy = %q", got)
	}
	var got struct {
		OK              bool   `json:"ok"`
		Daemon          string `json:"daemon"`
		Version         string `json:"version"`
		ProtocolVersion string `json:"protocolVersion"`
		SecretBackend   string `json:"secretBackend"`
		Runtime         struct {
			ConnectedClients int `json:"connectedClients"`
			SessionsListed   int `json:"sessionsListed"`
			ActiveSessions   int `json:"activeSessions"`
			StoredSessions   int `json:"storedSessions"`
			EndedSessions    int `json:"endedSessions"`
			Metrics          struct {
				AdapterStarts        int64            `json:"adapterStarts"`
				AdapterStartFailures int64            `json:"adapterStartFailures"`
				RouteFailures        int64            `json:"routeFailures"`
				RouteDecisions       map[string]int64 `json:"routeDecisions"`
			} `json:"metrics"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Daemon != "capd" || got.Version != "test-version" || got.ProtocolVersion != protocol.Version || got.SecretBackend != secret.BackendFile {
		t.Fatalf("health json = %+v", got)
	}
	if got.Runtime.ConnectedClients != 2 || got.Runtime.SessionsListed != 0 || got.Runtime.ActiveSessions != 0 || got.Runtime.StoredSessions != 0 || got.Runtime.EndedSessions != 0 {
		t.Fatalf("runtime health = %+v", got.Runtime)
	}
	if got.Runtime.Metrics.AdapterStarts != 2 || got.Runtime.Metrics.AdapterStartFailures != 1 || got.Runtime.Metrics.RouteFailures != 1 || got.Runtime.Metrics.RouteDecisions["codex"] != 1 {
		t.Fatalf("runtime metrics = %+v", got.Runtime.Metrics)
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
	for _, want := range []string{"CAPD Probe", "accounts/check", "accounts/list", "accounts/quota", "agents/usage", "agents/route", "/healthz?format=json", "/probe/data", "Authorization:`Bearer ${TOKEN}`", "fetchProbeData", "httpProbe", "fallbackResult", "fallbackHealth", "http probe error", "fetchHealthInfo", "healthEvidence", "health: healthInfo", "health: fallbackHealth", "protocolVersion", "secretBackend", "secretState", "credentialReadable", "runtimeReady", "requireMultiple", "requireAllFreshQuota", "Evidence JSON", "Validation Tests", "Next step", "nextStep", "nextSteps", "repairPlan", "repairStepClassification", "repairCommandBinary", "step.execution", "server classified repair step", "manual reason:", "outside repair runner allowlist", "requires --include-final", "expectedEvidence", "checks", "validationRows", "showTests", "daemon health", "account metadata", "account credentials", "account runtime", "not checked in prompt-free refresh", "safe no-prompt audit", "use Readiness only when ready for SecretStore credential checks", "use Readiness only when ready for runtime projection checks", "promptFreeDoctorCommand", "credentialAccessNextStep", "access-denied", "macOS Keychain access was denied", "approve macOS Keychain access, or restart with file SecretStore and re-import accounts", "project account runtimes with accounts/project", "multi-account readiness", "quota freshness", "auto route fresh", "require native secret", "native secret backend", "readiness gate", "rpcError", "e.data", "routeDecision = e.data", "e.data.accountRoute || route", "readinessCommand", "doctorCommand", "capd doctor --prompt-free --json --fail", "capd accounts check --json --readiness", "--require-secret-backend ${backend}", "--timeout 2m", "capd agents route --account auto --require-fresh-quota --json", "capd start --secret-backend native", "deep verify with:", "nativeSecretNextStep", "readinessError", "routeError", "routeDecision", "routeDecisionText", "routeCandidates", "route candidates", "routeCandidateText", "routePolicy", "route policy", "routePolicyText", "freshTtlSeconds", "unknownScore", "currentAccountTieBreak", "decision.reason", "readinessSummaryText", "http summary", "summary.ready", "summary.checkedAccounts", "summary.secretBackendOk", "RPC_TIMEOUT_MS = 12000", "LONG_RPC_TIMEOUT_MS = 120000", "rpcTimeoutFor", "lightweightAccountsResult", "accountQuotaState", "accountQuotaFresh", "accountQuotaCheckedAt", "accountPrimaryUsedPercent", "a.quotaState", "a.quotaFresh", "a.quotaCheckedAt", "a.primaryUsedPercent", `call("accounts/list", { provider:"codex" })`, "http probe: skipped for prompt-free refresh", `method === "accounts/check" && (params.refreshQuota || params.requireFreshQuota || params.requireAllFreshQuota || params.requireMultiple)`, `timeout after ${timeoutMS}ms`, "clearTimeout(pending.timer)"} {
		if !strings.Contains(body, want) {
			t.Fatalf("probe HTML missing %q", want)
		}
	}
	for _, forbidden := range []string{"secretRef", "secret_ref", "rawAuthJson", "RawAuthJSON", "localStorage.setItem", "sessionStorage.setItem", "?token=${TOKEN}", `id="native" type="checkbox" checked`, `call("accounts/check", { provider:"codex" })`} {
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

func TestProbeValidationRowsStayUnique(t *testing.T) {
	body := probeHTML
	for _, name := range []string{
		`name:"daemon health"`,
		`name:"account credentials"`,
		`name:"account runtime"`,
		`name:"multi-account readiness"`,
		`name:"quota freshness"`,
		`name:"auto route data"`,
		`name:"auto route fresh"`,
		`name:"route decision"`,
		`name:"route candidates"`,
		`name:"route policy"`,
		`name:"native secret backend"`,
	} {
		if got := strings.Count(body, name); got != 1 {
			t.Fatalf("probe validation row %q count = %d", name, got)
		}
	}
	for _, dynamic := range []string{
		`"account metadata"`,
		`"accounts/check data"`,
	} {
		if got := strings.Count(body, dynamic); got != 1 {
			t.Fatalf("probe dynamic account data label %q count = %d", dynamic, got)
		}
	}
	for _, dynamic := range []string{
		`name:"readiness gate"`,
		`name:"readiness error detail"`,
		`name:"route error detail"`,
	} {
		if got := strings.Count(body, dynamic); got != 1 {
			t.Fatalf("probe dynamic validation row %q count = %d", dynamic, got)
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

func TestProbeDataAcceptsScopedProbeReadToken(t *testing.T) {
	s, _ := newTestServer(t)
	token, err := security.MintScopedToken("test-token", security.TokenScopeProbeRead, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/probe/data", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.handleProbeData(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "test-token") || strings.Contains(rec.Body.String(), token) {
		t.Fatalf("probe data leaked token: %s", rec.Body.String())
	}
}

func TestScopedProbeTokenCannotCallSessionCreate(t *testing.T) {
	s, _ := newTestServer(t)
	client := &wsClient{auth: authInfo{Scope: security.TokenScopeProbeRead}}
	resp := s.dispatch(context.Background(), client, testRequest(protocol.MethodInitialize, protocol.InitializeParams{ProtocolVersion: protocol.Version}))
	if resp.Error != nil {
		t.Fatalf("initialize error = %+v", resp.Error)
	}
	resp = s.dispatch(context.Background(), client, testRequest(protocol.MethodSessionCreate, protocol.SessionCreateParams{AgentID: "fake"}))
	if resp.Error == nil || resp.Error.Code != protocol.CodeUnauthorized || !strings.Contains(resp.Error.Message, "probe:read") {
		t.Fatalf("session/create response = %+v", resp)
	}
}

func TestScopedConsoleTokenAllowsAccountWorkflowButNotTaskControl(t *testing.T) {
	s, _ := newTestServer(t)
	client := &wsClient{auth: authInfo{Scope: security.TokenScopeConsole}}
	resp := s.dispatch(context.Background(), client, testRequest(protocol.MethodInitialize, protocol.InitializeParams{ProtocolVersion: protocol.Version}))
	if resp.Error != nil {
		t.Fatalf("initialize error = %+v", resp.Error)
	}
	resp = s.dispatch(context.Background(), client, testRequest(protocol.MethodAccountsImport, protocol.AccountsImportParams{}))
	if resp.Error == nil || resp.Error.Code == protocol.CodeUnauthorized {
		t.Fatalf("accounts/import should pass scope gate and fail later if invalid: %+v", resp)
	}
	resp = s.dispatch(context.Background(), client, testRequest(protocol.MethodTaskSend, protocol.TaskSendParams{SessionID: "s_test", Prompt: "x"}))
	if resp.Error == nil || resp.Error.Code != protocol.CodeUnauthorized || !strings.Contains(resp.Error.Message, security.TokenScopeConsole) {
		t.Fatalf("task/send response = %+v", resp)
	}
}

func TestProbeDataRejectsUnknownRequiredSecretBackend(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/probe/data?readiness=1&requireSecretBackend=mystery", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	s.handleProbeData(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `unknown secret backend \"mystery\"`) || !strings.Contains(body, `"ok":false`) {
		t.Fatalf("body = %s", body)
	}
	if strings.Contains(body, "test-token") {
		t.Fatalf("body leaked token: %s", body)
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
	if !got.OK || !got.PromptFree {
		t.Fatalf("probe data not ok: %+v body=%s", got.Errors, rec.Body.String())
	}
	if got.AccountsCheck == nil || got.AccountsCheck.CheckedAccounts != 1 {
		t.Fatalf("accountsCheck = %+v", got.AccountsCheck)
	}
	if check := probeCheckByName(got.Checks, "account credentials"); !check.OK || !strings.Contains(check.Evidence, "not checked in prompt-free probe") {
		t.Fatalf("credential check = %+v", check)
	}
	if check := probeCheckByName(got.Checks, "account runtime"); !check.OK || !strings.Contains(check.Evidence, "not checked in prompt-free probe") {
		t.Fatalf("runtime check = %+v", check)
	}
	if got.RouteDecision == nil || got.RouteDecision.AccountID != "codex-test" {
		t.Fatalf("routeDecision = %+v", got.RouteDecision)
	}
	if got.AutoRoute == nil || got.AutoRoute.AccountID != "codex-test" || got.AutoRoute.SecretBackend != secret.BackendFile || !got.AutoRoute.Fresh || got.AutoRoute.Reason != "auto account codex-test primary 12%; current account tie-break" {
		t.Fatalf("autoRoute = %+v", got.AutoRoute)
	}
	if len(got.RouteCandidates) != 1 || got.RouteCandidates[0].AccountID != "codex-test" || got.RouteCandidates[0].SecretBackend != secret.BackendFile || got.RouteCandidates[0].Reason != "auto account codex-test primary 12%; current account tie-break" {
		t.Fatalf("routeCandidates = %+v", got.RouteCandidates)
	}
	if got.RoutePolicy == nil || got.RoutePolicy.Name != "conservative-quota-pressure" || got.RoutePolicy.FreshTTLSeconds != 1800 || got.RoutePolicy.UnknownScore != 75 || got.RoutePolicy.CurrentAccountTieBreak != 0.01 || !containsString(got.RoutePolicy.QuotaWindows, "code_review") {
		t.Fatalf("routePolicy = %+v", got.RoutePolicy)
	}
	if check := probeCheckByName(got.Checks, "route policy"); !check.OK || !strings.Contains(check.Evidence, "conservative-quota-pressure") || !strings.Contains(check.Evidence, "windows primary,secondary,code_review") {
		t.Fatalf("route policy check = %+v", check)
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
	if check := probeCheckByName(got.Checks, "account credentials"); !check.OK || !strings.Contains(check.Evidence, "readable 1/1") {
		t.Fatalf("credential check = %+v", check)
	}
	if check := probeCheckByName(got.Checks, "account runtime"); !check.OK || !strings.Contains(check.Evidence, "runtime 1/1") {
		t.Fatalf("runtime check = %+v", check)
	}
	if got.Summary.Ready || !got.Summary.Readiness || got.Summary.CheckedAccounts != 1 || got.Summary.MissingAccounts != 1 || got.Summary.FreshQuotaAccounts != 1 || got.Summary.RequiredSecretBackend != "file" || got.Summary.SecretBackend != "file" || !got.Summary.SecretBackendOK {
		t.Fatalf("partial summary = %+v", got.Summary)
	}
	if !got.Summary.QuotaRefreshed || got.Summary.QuotaRefreshed != got.AccountsCheck.Summary.QuotaRefreshed {
		t.Fatalf("partial summary did not reuse accounts/check quota refresh evidence: probe=%+v accounts=%+v", got.Summary, got.AccountsCheck.Summary)
	}
	if len(got.Errors) == 0 || !strings.Contains(got.Errors[0].Message, "expected multiple Codex accounts") {
		t.Fatalf("errors = %+v", got.Errors)
	}
	if !strings.Contains(rec.Body.String(), "multi-account readiness") {
		t.Fatalf("body missing readiness checks: %s", rec.Body.String())
	}
	wantNext := multiAccountImportNextStep()
	if !probeNextStepsContain(got.NextSteps, wantNext) {
		t.Fatalf("nextSteps missing %q: %+v", wantNext, got.NextSteps)
	}
	for _, want := range []protocol.RepairStep{
		{ID: "import-codex-accounts", Command: "capd accounts import --auth /path/a/auth.json --auth /path/b/auth.json", RequiresDaemon: true, RequiresSecret: true},
		{ID: "final-live-preflight", Command: "make live-codex-preflight", RequiresDaemon: true, RequiresSecret: true},
	} {
		if !probeRepairPlanContains(got.RepairPlan, want) {
			t.Fatalf("repairPlan missing %+v: %+v", want, got.RepairPlan)
		}
	}
	importStep := probeRepairStepByID(got.RepairPlan, "import-codex-accounts")
	if importStep.Execution == nil || importStep.Execution.Runnable || !strings.Contains(importStep.Execution.Reason, "placeholders") {
		t.Fatalf("import repair execution = %+v", importStep.Execution)
	}
	finalStep := probeRepairStepByID(got.RepairPlan, "final-live-preflight")
	if finalStep.Execution == nil || finalStep.Execution.Runnable || !strings.Contains(finalStep.Execution.Reason, "--include-final") {
		t.Fatalf("final repair execution = %+v", finalStep.Execution)
	}
}

func probeNextStepsContain(steps []string, want string) bool {
	for _, step := range steps {
		if step == want {
			return true
		}
	}
	return false
}

func probeRepairPlanContains(steps []protocol.RepairStep, want protocol.RepairStep) bool {
	for _, step := range steps {
		if want.ID != "" && step.ID != want.ID {
			continue
		}
		if want.Command != "" && step.Command != want.Command {
			continue
		}
		if want.ExpectedEvidence != "" && step.ExpectedEvidence != want.ExpectedEvidence {
			continue
		}
		if want.RequiresDaemon && !step.RequiresDaemon {
			continue
		}
		if want.RequiresSecret && !step.RequiresSecret {
			continue
		}
		return true
	}
	return false
}

func probeRepairStepByID(steps []protocol.RepairStep, id string) protocol.RepairStep {
	for _, step := range steps {
		if step.ID == id {
			return step
		}
	}
	return protocol.RepairStep{}
}

func TestProbeDataNextStepsExplainMacOSKeychainCancellation(t *testing.T) {
	steps := probeDataNextSteps(nil, []probeDataError{{
		Source:  "accounts/check",
		Code:    protocol.CodeInternalError,
		Message: "refresh quota: codex-test: load account secret: macOS keychain status -128",
	}})
	if len(steps) != 1 {
		t.Fatalf("steps = %+v", steps)
	}
	for _, want := range []string{"macOS Keychain denied or canceled credential access", "approve the prompt", "capd start --secret-backend file", "capd accounts --secret-backend file codex import"} {
		if !strings.Contains(steps[0], want) {
			t.Fatalf("step missing %q: %q", want, steps[0])
		}
	}
}

func TestProbeDataNextStepsUseRunnableNativeSecretStoreRetry(t *testing.T) {
	steps := probeDataNextSteps(nil, []probeDataError{{
		Source:  "accounts/check",
		Code:    protocol.CodeInternalError,
		Message: "refresh quota: codex-test: load account secret: keychain interaction timed out",
	}})
	want := "unlock or approve OS SecretStore access, then rerun: capd probe data --json --readiness --require-secret-backend native --timeout 2m --fail"
	if len(steps) != 1 || steps[0] != want {
		t.Fatalf("steps = %+v, want %q", steps, want)
	}
}

func TestAccountSecretErrorStateClassifiesMacOSKeychainCancellation(t *testing.T) {
	got := accountSecretErrorState(errors.New("load account secret: macOS keychain status -128"))
	if got != protocol.AccountSecretStateAccessDenied {
		t.Fatalf("state = %q, want %q", got, protocol.AccountSecretStateAccessDenied)
	}
}

func TestAccountSecretStatesEvidenceIncludesAccessDenied(t *testing.T) {
	got := accountSecretStatesEvidence([]protocol.AccountCheckEvidence{
		{SecretState: protocol.AccountSecretStateAccessDenied},
		{SecretState: protocol.AccountSecretStateUnreadable},
	})
	if got != "access-denied 1, unreadable 1" {
		t.Fatalf("evidence = %q", got)
	}
}

func TestProbeCredentialNextStepUsesSecretState(t *testing.T) {
	cases := []struct {
		name    string
		state   string
		backend string
		want    []string
	}{
		{
			name:    "unreadable",
			state:   protocol.AccountSecretStateUnreadable,
			backend: secret.BackendNative,
			want:    []string{"capd secretstore check --json --roundtrip --secret-backend native --require-backend native --timeout 2m", "re-import affected accounts through CAP"},
		},
		{
			name:    "timeout",
			state:   protocol.AccountSecretStateTimeout,
			backend: secret.BackendNative,
			want:    []string{"unlock or approve OS SecretStore access", "capd probe data --json --readiness --require-secret-backend native --timeout 2m --fail"},
		},
		{
			name:  "access denied",
			state: protocol.AccountSecretStateAccessDenied,
			want:  []string{"macOS Keychain access was denied or canceled", "capd start --secret-backend file"},
		},
		{
			name:  "backend mismatch",
			state: protocol.AccountSecretStateBackendMismatch,
			want:  []string{"restart daemon with the account SecretStore backend", "capd accounts import --auth /path/to/auth.json"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := probeCredentialNextStep([]protocol.AccountCheckEvidence{{SecretState: tc.state}}, tc.backend)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("next step missing %q: %q", want, got)
				}
			}
		})
	}
	if got := probeSecretStoreCheckCommand(""); got != "capd secretstore check --json --roundtrip --timeout 2m" {
		t.Fatalf("generic secretstore command = %q", got)
	}
}

func TestProbeDataReadinessDefaultsToNativeAndAvoidsQuotaOnBackendMismatch(t *testing.T) {
	s, _, _, accounts := newCodexAccountIntegrationServer(t)
	acc, err := accounts.LoadAccount("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	acc.SecretRef = secret.BackendNative + ":codex-test"
	if err := accounts.UpsertAccount(acc); err != nil {
		t.Fatal(err)
	}
	var quotaCalls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		quotaCalls.Add(1)
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer backend.Close()
	s.opts.CodexQuotaBaseURL = backend.URL

	req := httptest.NewRequest(http.MethodGet, "/probe/data?readiness=1", nil)
	req.Header.Set("Authorization", "Bearer it-token")
	rec := httptest.NewRecorder()
	s.handleProbeData(rec, req)
	if rec.Code != http.StatusFailedDependency {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if quotaCalls.Load() != 0 {
		t.Fatalf("quota calls = %d", quotaCalls.Load())
	}
	var got probeDataResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK {
		t.Fatalf("readiness unexpectedly ok: %+v", got)
	}
	if got.Summary.RequiredSecretBackend != secret.BackendNative || got.Summary.SecretBackend != secret.BackendFile || got.Summary.SecretBackendOK {
		t.Fatalf("summary = %+v", got.Summary)
	}
	if got.AutoRoute == nil || got.AutoRoute.AccountID != "codex-test" || got.AutoRoute.SecretBackend != secret.BackendNative || got.AutoRoute.QuotaState != protocol.AccountQuotaStateMissing || got.AutoRoute.Fresh || got.AutoRoute.Reason != "auto account codex-test without fresh cached quota; current account tie-break" {
		t.Fatalf("autoRoute partial route evidence = %+v", got.AutoRoute)
	}
	if len(got.RouteCandidates) != 1 || got.RouteCandidates[0].AccountID != "codex-test" || got.RouteCandidates[0].SecretBackend != secret.BackendNative || got.RouteCandidates[0].QuotaState != protocol.AccountQuotaStateMissing || got.RouteCandidates[0].Reason != "auto account codex-test without fresh cached quota; current account tie-break" {
		t.Fatalf("routeCandidates partial route evidence = %+v", got.RouteCandidates)
	}
	if got.Summary.AutoRouteAccountID != "codex-test" || got.Summary.AutoRouteFresh || got.Summary.RouteCandidates != 1 || got.Summary.RouteDecisionOK {
		t.Fatalf("summary route evidence = %+v", got.Summary)
	}
	if check := probeCheckByName(got.Checks, "SecretStore backend"); check.OK || !strings.Contains(check.Evidence, "secret file, want native") || !strings.Contains(check.NextStep, "capd start --secret-backend native") {
		t.Fatalf("SecretStore backend check = %+v", check)
	}
	wantRouteNext := "refresh and verify daemon-side readiness with: capd accounts check --json --readiness --require-secret-backend native --timeout 2m"
	for _, name := range []string{"quota freshness", "auto route fresh"} {
		if check := probeCheckByName(got.Checks, name); check.OK || check.NextStep != wantRouteNext {
			t.Fatalf("%s check = %+v", name, check)
		}
	}
	if !probeNextStepsContain(got.NextSteps, wantRouteNext) {
		t.Fatalf("nextSteps missing route-backend readiness command %q: %+v", wantRouteNext, got.NextSteps)
	}
	if len(got.Errors) == 0 || got.Errors[0].Source != "accounts/check" || !strings.Contains(got.Errors[0].Message, `secret backend = "file", want "native"`) {
		t.Fatalf("errors = %+v", got.Errors)
	}
	body := rec.Body.String()
	for _, leaked := range []string{"it-token", "test-token", "secretRef", "file:codex-test", "CODEX_HOME", s.opts.RuntimeRoot} {
		if strings.Contains(body, leaked) {
			t.Fatalf("probe data leaked %q: %s", leaked, body)
		}
	}
}

func TestProbeDataReportsCredentialAndRuntimeFailures(t *testing.T) {
	s, _, _, accounts := newCodexAccountIntegrationServer(t)
	acc, err := accounts.LoadAccount("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	acc.SecretRef = secret.BackendNative + ":codex-test"
	if err := accounts.UpsertAccount(acc); err != nil {
		t.Fatal(err)
	}
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
		t.Fatalf("probe data unexpectedly ok: %+v", got)
	}
	if got.PromptFree {
		t.Fatalf("readiness probe should not be prompt-free: %+v", got)
	}
	if got.AccountsCheck == nil || len(got.AccountsCheck.Accounts) != 1 || got.AccountsCheck.Accounts[0].SecretBackendOK {
		t.Fatalf("accountsCheck = %+v", got.AccountsCheck)
	}
	if check := probeCheckByName(got.Checks, "account credentials"); check.OK || !strings.Contains(check.Evidence, "readable 0/1") || !strings.Contains(check.Evidence, "backend 0/1") || !strings.Contains(check.NextStep, "SecretStore") {
		t.Fatalf("credential check = %+v", check)
	}
	if check := probeCheckByName(got.Checks, "account runtime"); check.OK || !strings.Contains(check.Evidence, "runtime 0/1") {
		t.Fatalf("runtime check = %+v", check)
	}
	body := rec.Body.String()
	for _, leaked := range []string{"it-token", "test-token", "native:codex-test", "secretRef", "rawAuthJson", s.opts.RuntimeRoot} {
		if strings.Contains(body, leaked) {
			t.Fatalf("probe data leaked %q: %s", leaked, body)
		}
	}
}

func probeCheckByName(checks []probeDataCheck, name string) probeDataCheck {
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	return probeDataCheck{}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestConsoleStaticContract(t *testing.T) {
	html := consoleHTML
	required := []string{
		`value="auto"`,
		"自动账号",
		`id="accountProfile"`,
		"profile（路由仅自动账号）",
		"accounts/list",
		`call("accounts/list", {})`,
		`call("accounts/list", { provider: "codex" })`,
		"accounts/import",
		"accounts/current",
		"accounts/project",
		"accounts/check",
		"accounts/quota",
		"accounts/remove",
		"profiles/list",
		"profiles/update",
		"profiles/members",
		"agents/route",
		"accountRoute",
		"previewRoute",
		"accountProfileName",
		"updateProfileControl",
		"renderProfiles",
		"saveProfile",
		"updateProfileMember",
		"deleteProfile",
		`params.profile = profile`,
		`create.profile = profile`,
		"routePreview",
		"routeDecisionSummary",
		"route ${routeDecisionSummary(route)}",
		"routeCandidateSummary",
		"routeCandidates",
		"route.secretBackend",
		"route candidates",
		"routeError",
		"route error ${routeError}",
		"if (e.data) route = e.data",
		"const partial = e.data ?",
		"readiness",
		"accountDiagnostics",
		"accountDiagnosticsEl",
		"readinessChecks",
		"repairPlan",
		"repairPlanEl",
		"evidencePackage",
		"evidenceManifestInput",
		"evidenceArtifactsInput",
		"renderEvidenceManifest",
		"clearEvidenceManifest",
		"report.html path is shown, not loaded",
		"normalizeEvidenceManifest",
		"evidenceManifestRows",
		"parseEvidenceArtifacts",
		"evidenceArtifactRows",
		"routeDecisionEvidenceOK",
		"routeDecisionEvidenceText",
		"quotaEvidenceOK",
		"quotaEvidenceText",
		"renderEvidenceManifestRows",
		"manifestVersion",
		"summaryVersion",
		"evidenceManifestPath",
		"evidenceReportPath",
		"agentsRoute",
		"HTML report",
		`["report", "HTML report"]`,
		"probeData",
		"CAPD_LIVE_EVIDENCE_DIR",
		"diagnostic package",
		"artifact coverage",
		"route policy evidence",
		"route candidate evidence",
		"route decision evidence",
		"quota freshness evidence",
		"repair plan evidence",
		"paste route/probe/doctor JSON",
		"renderRepairPlan",
		"renderAccountsCheckRepairPlan",
		"renderAccountsCheckRepairPlan(result)",
		"renderAccountsCheckRepairPlan(e.data)",
		"repairStepClassification",
		"repairCommandBinary",
		"step.execution",
		"server classified repair step",
		"runnable with: capd repair run --execute --yes",
		"manual: ${classification.reason}",
		"manual reason ${classification.reason}",
		"command contains placeholders",
		"starts a foreground daemon",
		"outside repair runner allowlist",
		"requires --include-final",
		".repair",
		".repair.runnable",
		".repair.manual",
		"expectedEvidence",
		"readinessCheckRows",
		"renderReadinessChecks",
		"renderAccountDiagnostics",
		"accountDiagnosticSummary",
		"accountDiagnosticCard",
		"accountPromptFreeNextStep",
		"accountQuotaDiagnosticText",
		"accountDiagnosticIssue",
		"accountDiagnosticNextStep",
		"secretAccessNextStep",
		"access-denied",
		"macOS Keychain access was denied",
		"boolWord",
		"lightweightAccountsResult",
		"lightweightAccountsSummary",
		"promptFree",
		`promptFree ? "accounts/list" : "accounts/check"`,
		"not ready",
		"quota refreshed",
		"secret not checked in prompt-free refresh",
		"runtime not checked in prompt-free refresh",
		"safe no-prompt audit",
		"explicit readiness checks may read SecretStore credentials",
		"secret ${account.secretState",
		"runtime ${boolWord(account.runtimeReady)}",
		"approve macOS Keychain access, or restart with file SecretStore and re-import",
		"refresh quota for all accounts",
		"Codex multi-account import",
		"Codex quota freshness",
		"Codex auto route freshness",
		"SecretStore backend",
		"导入至少两个账号",
		"readinessCommand",
		"doctorCommand",
		"promptFreeDoctorCommand",
		"capd doctor --prompt-free --json --fail",
		"capd accounts check --json --readiness",
		"--require-secret-backend ${backend}",
		"--timeout 2m",
		"capd agents route --account auto --require-fresh-quota --json",
		"refreshDiagnostic",
		"刷新诊断",
		"deepVerify",
		"深度验证",
		"fetchProbeData",
		"headers: { Authorization: `Bearer ${TOKEN}` }",
		`url.searchParams.set("readiness", "1")`,
		`url.searchParams.set("requireSecretBackend", requireSecretBackend)`,
		"renderProbeDataResult",
		"probe summary ready=",
		"data.nextSteps",
		"probeErrorSummary",
		"missing daemon token for /probe/data",
		"refreshReadinessDiagnostic",
		"REQUIRED_SECRET_BACKEND",
		"normalizeSecretBackend",
		"requiredSecretBackend()",
		`params.get("requireSecretBackend")`,
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
		"routePolicySummary",
		"freshTtlSeconds",
		"unknownScore",
		"currentAccountTieBreak",
		"route.primaryUsedPercent",
		"route.secondaryUsedPercent",
		"route.codeReviewUsedPercent",
		"route.limitingUsedPercent",
		"route.limitingQuotaDimension",
		"route.checkedAt",
		"routeAuditLabel",
		"a.routeScore",
		"a.routeReason",
		"route score ${a.routeScore}",
		"codexAccounts",
		"暂无账号，先导入 Codex",
		"暂无 Codex 账号，先导入 Codex",
		"refreshSelectedQuota",
		"refreshAllQuota",
		"刷新全部 quota",
		"quota 批量刷新完成",
		"quota 批量刷新失败",
		"renderQuotaAllPartialFailure",
		"quota partial refreshed",
		"quota partial evidence",
		"quota failed account",
		"data.failedAccount",
		"data.nextSteps",
		`call("accounts/quota", { provider: "codex", accountId: "all" })`,
		"importCodexAccount",
		"导入 Codex",
		"多个路径用换行或逗号分隔",
		"splitAuthPaths",
		`split(/\r?\n|,/)`,
		"params.authPaths = authPaths",
		"result.accounts",
		"result.importedAccounts",
		"accountsImportNextStep",
		"继续导入第二个 Codex 账号",
		"import a second Codex account, then run safe no-prompt audit",
		"CAPD_CODEX_AUTH_PATHS",
		"macOS/Linux",
		"Windows",
		"运行就绪门禁或 ",
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
		"a.checkedAt",
		"not-fresh",
		"result.quotaRefreshed",
		"params.refreshQuota = true",
		"params.requireMultiple = true",
		"params.requireFreshQuota = true",
		"params.requireAllFreshQuota = true",
		"params.requireSecretBackend = requiredBackend",
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
		"accountQuotaState(a)",
		"q.quotaState",
		`return "missing"`,
		"quotaFresh(q)",
		"accountQuotaFresh(a)",
		"accountQuotaCheckedAt(a)",
		"accountPrimaryUsedPercent(a)",
		`? "fresh" : "stale"`,
		"hasNumber(primary)",
		"s.accountId",
		"button.disabled = true",
		"rejectAllPending",
		"rpcError",
		"error.data",
		"e.data",
		"partial evidence",
		"renderReadinessDiagnosticResult",
		"readinessSummaryText",
		"summary ready",
		"accounts ${checked}/${required}",
		"missing quota",
		"staleQuotaAccounts",
		"missingQuotaAccounts",
		"missingAccounts: Math.max(0, required - checked)",
		"summary.routeDecisionOk",
		"route decision",
		"secret ok",
		"readinessError",
		"readiness gate",
		"requireNativeSecret",
		"SecretStore 未启用",
		"deep verify with:",
		"approve macOS Keychain access, or restart with file SecretStore and re-import failing accounts",
		"capd start --secret-backend ${requiredBackend}",
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
	start := strings.Index(html, "async function refreshReadinessDiagnostic")
	if start < 0 {
		t.Fatal("console HTML missing refreshReadinessDiagnostic")
	}
	end := strings.Index(html[start:], "async function deepVerify")
	if end < 0 {
		t.Fatal("console HTML missing refreshReadinessDiagnostic terminator")
	}
	refreshBlock := html[start : start+end]
	for _, want := range []string{
		`const listResult = codexListResult || await call("accounts/list", { provider: "codex" })`,
		"lightweightAccountsResult(listResult, healthInfo)",
		`call("agents/route", { accountId: "auto" })`,
	} {
		if !strings.Contains(refreshBlock, want) {
			t.Fatalf("prompt-free console diagnostic missing %q", want)
		}
	}
	if strings.Contains(refreshBlock, `call("accounts/check"`) {
		t.Fatal("prompt-free console diagnostic must not call accounts/check")
	}
}

func TestConsoleApprovalRendererHasSingleBoxDeclaration(t *testing.T) {
	const declaration = `const box = document.createElement("div");`
	start := strings.Index(consoleHTML, "function renderApproval(d) {")
	if start < 0 {
		t.Fatal("console HTML missing renderApproval")
	}
	end := strings.Index(consoleHTML[start:], "\nfunction clearLog()")
	if end < 0 {
		t.Fatal("console HTML missing renderApproval terminator")
	}
	got := strings.Count(consoleHTML[start:start+end], declaration)
	if got != 1 {
		t.Fatalf("console approval renderer box declaration count = %d, want 1", got)
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
