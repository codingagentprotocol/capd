package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeDataCmdUsesAuthorizationHeader(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-probe-secret"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	var sawAuth, rawQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/probe/data" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		sawAuth = r.Header.Get("Authorization")
		rawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"summary":{"ready":true,"readiness":true,"checkedAccounts":2,"requiredAccounts":2,"missingAccounts":0,"freshQuotaAccounts":2,"staleQuotaAccounts":0,"missingQuotaAccounts":0,"autoRouteAccountId":"codex-a","autoRouteFresh":true,"routeDecisionOk":true,"routeCandidates":2,"secretBackend":"native","secretBackendOk":true},"health":{"version":"test","protocolVersion":"0.1","secretBackend":"native"},"accountsCheck":{"provider":"codex","secretBackend":"native","checkedAccounts":2},"autoRoute":{"accountId":"codex-a","quotaState":"fresh","fresh":true},"checks":[{"name":"daemon health","ok":true,"evidence":"health ok"}]}`))
	}))
	defer ts.Close()
	host, port := splitTestURL(t, ts.URL)
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)

	var out bytes.Buffer
	cmd := newProbeCmd()
	cmd.SetArgs([]string{"data", "--json", "--readiness", "--require-secret-backend", "native", "--timeout", "5s", "--fail"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if sawAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q", sawAuth)
	}
	if strings.Contains(rawQuery, token) || !strings.Contains(rawQuery, "readiness=1") || !strings.Contains(rawQuery, "requireSecretBackend=native") {
		t.Fatalf("query = %q", rawQuery)
	}
	text := out.String()
	if !strings.Contains(text, `"ok": true`) || !strings.Contains(text, `"checkedAccounts": 2`) || !strings.Contains(text, `"missingAccounts": 0`) {
		t.Fatalf("output = %s", text)
	}
	if strings.Contains(text, token) {
		t.Fatalf("output leaked token: %s", text)
	}
}

func TestProbeDataTextPrintsReadinessSummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeTokenForTest(home, "tok-probe-summary"); err != nil {
		t.Fatal(err)
	}
	var rawQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"summary":{"ready":true,"readiness":true,"checkedAccounts":2,"requiredAccounts":2,"missingAccounts":0,"freshQuotaAccounts":2,"staleQuotaAccounts":0,"missingQuotaAccounts":0,"autoRouteAccountId":"codex-a","autoRouteFresh":true,"routeDecisionOk":true,"routeCandidates":2,"secretBackend":"native","requiredSecretBackend":"native","secretBackendOk":true},"health":{"version":"test","protocolVersion":"0.1","secretBackend":"native"},"accountsCheck":{"provider":"codex","secretBackend":"native","checkedAccounts":2},"autoRoute":{"accountId":"codex-a","quotaState":"fresh","fresh":true},"checks":[{"name":"daemon health","ok":true,"evidence":"health ok"}]}`))
	}))
	defer ts.Close()
	host, port := splitTestURL(t, ts.URL)
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)

	var out bytes.Buffer
	cmd := newProbeCmd()
	cmd.SetArgs([]string{"data", "--readiness"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rawQuery, "readiness=1") || !strings.Contains(rawQuery, "requireSecretBackend=native") {
		t.Fatalf("query = %q", rawQuery)
	}
	text := out.String()
	for _, want := range []string{"summary: ready=true accounts=2/2 missing=0 quota fresh=2 stale=0 missing=0 autoFresh=true routeDecision=true routeCandidates=2 secretOK=true", "secret backend: actual=native required=native ok=true", "auto route: codex-a fresh fresh=true"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q: %s", want, text)
		}
	}
}

func TestProbeDataTextPrintsPartialRouteCandidates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-probe-candidates"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusFailedDependency)
		w.Write([]byte(`{"ok":false,"summary":{"ready":false,"readiness":true,"checkedAccounts":2,"requiredAccounts":2,"missingAccounts":0,"freshQuotaAccounts":0,"staleQuotaAccounts":1,"missingQuotaAccounts":1,"autoRouteAccountId":"codex-a","autoRouteFresh":false,"routeDecisionOk":false,"routeCandidates":2,"secretBackend":"native","requiredSecretBackend":"native","secretBackendOk":true},"health":{"version":"test","protocolVersion":"0.1","secretBackend":"native"},"autoRoute":{"accountId":"codex-a","quotaState":"stale","fresh":false,"primaryUsedPercent":34.5},"routeCandidates":[{"accountId":"codex-a","quotaState":"stale","fresh":false,"primaryUsedPercent":34.5,"score":50,"reason":"stale quota"},{"accountId":"codex-b","quotaState":"missing","fresh":false,"score":50,"reason":"missing quota"}],"checks":[{"name":"Codex auto route freshness","ok":false,"evidence":"codex-a stale fresh=false","nextStep":"refresh quota"}],"nextSteps":["refresh quota"],"errors":[{"source":"agents/route","code":-32602,"message":"auto account codex-a without fresh cached quota"}]}`))
	}))
	defer ts.Close()
	host, port := splitTestURL(t, ts.URL)
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)

	var out bytes.Buffer
	cmd := newProbeCmd()
	cmd.SetArgs([]string{"data", "--readiness"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"status: 424",
		"auto route: codex-a stale fresh=false",
		"route candidates: codex-a stale fresh=false primary=34.5% stale quota; codex-b missing fresh=false missing quota",
		"error: agents/route code=-32602 auto account codex-a without fresh cached quota",
		"next: refresh quota",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, token) {
		t.Fatalf("output leaked token: %s", text)
	}
}

func TestProbeDataReadinessCanOverrideRequiredSecretBackend(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeTokenForTest(home, "tok-probe-file"); err != nil {
		t.Fatal(err)
	}
	var rawQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"summary":{"ready":true,"readiness":true,"checkedAccounts":2,"requiredAccounts":2,"missingAccounts":0,"freshQuotaAccounts":2,"staleQuotaAccounts":0,"missingQuotaAccounts":0,"autoRouteAccountId":"codex-a","autoRouteFresh":true,"routeDecisionOk":true,"routeCandidates":2,"secretBackend":"file","requiredSecretBackend":"file","secretBackendOk":true},"health":{"version":"test","protocolVersion":"0.1","secretBackend":"file"},"checks":[{"name":"daemon health","ok":true,"evidence":"health ok"}]}`))
	}))
	defer ts.Close()
	host, port := splitTestURL(t, ts.URL)
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)

	cmd := newProbeCmd()
	cmd.SetArgs([]string{"data", "--readiness", "--require-secret-backend", "file"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rawQuery, "readiness=1") || !strings.Contains(rawQuery, "requireSecretBackend=file") || strings.Contains(rawQuery, "requireSecretBackend=native") {
		t.Fatalf("query = %q", rawQuery)
	}
}

func TestProbeDataTextPrintsHTTPJSONError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-probe-json-error"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"ok":false,"error":"unknown secret backend \"mystery\""}`))
	}))
	defer ts.Close()
	host, port := splitTestURL(t, ts.URL)
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)

	var out bytes.Buffer
	cmd := newProbeCmd()
	cmd.SetArgs([]string{"data"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"status: 400", "ok: false", `error: probe data unknown secret backend "mystery"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, token) {
		t.Fatalf("output leaked token: %s", text)
	}
}

func TestProbeDataCmdFailReportsNotReady(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-probe-fail"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusFailedDependency)
		w.Write([]byte(`{"ok":false,"health":{"version":"test","protocolVersion":"0.1","secretBackend":"file"},"accountsCheck":{"provider":"codex","secretBackend":"file","checkedAccounts":1},"checks":[{"name":"multi-account readiness","ok":false,"evidence":"checked 1, need 2","nextStep":"import another account"}],"errors":[{"source":"accounts/check","code":-32602,"message":"expected multiple Codex accounts, found 1"}]}`))
	}))
	defer ts.Close()
	host, port := splitTestURL(t, ts.URL)
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)

	var out bytes.Buffer
	cmd := newProbeCmd()
	cmd.SetArgs([]string{"data", "--json", "--readiness", "--fail"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "probe data failed") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), "multi-account readiness") {
		t.Fatalf("output = %s", out.String())
	}
	if strings.Contains(out.String(), token) {
		t.Fatalf("output leaked token: %s", out.String())
	}
}

func TestProbeDataCmdRejectsUnauthorized(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeTokenForTest(home, "tok-probe-unauthorized"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"ok":false,"error":"unauthorized"}`))
	}))
	defer ts.Close()
	host, port := splitTestURL(t, ts.URL)
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)

	cmd := newProbeCmd()
	cmd.SetArgs([]string{"data"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("err = %v", err)
	}
}
