package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestProbeEvidenceCmdSummarizesManifestArtifacts(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	route := filepath.Join(dir, "agents-route.json")
	probe := filepath.Join(dir, "probe-data-readiness.json")
	doctor := filepath.Join(dir, "doctor-prompt-free.json")
	writeTestFile(t, manifest, `{"manifestVersion":1,"status":"passed","stage":"complete","backend":"native","daemonMode":"temporary","artifacts":{"agentsRoute":"agents-route.json","probeData":"probe-data-readiness.json","doctor":"doctor-prompt-free.json"}}`)
	writeTestFile(t, route, `{"routePolicy":{"name":"conservative-quota-pressure","freshTtlSeconds":1800,"unknownScore":75,"currentAccountTieBreak":0.01,"quotaWindows":["primary","secondary","code_review"]},"routeCandidates":[{"accountId":"codex-a","quotaState":"fresh","fresh":true,"secretBackend":"native","primaryUsedPercent":12}]}`)
	writeTestFile(t, probe, `{"summary":{"checkedAccounts":2,"freshQuotaAccounts":2,"staleQuotaAccounts":0,"missingQuotaAccounts":0,"autoRouteFresh":true},"repairPlan":[]}`)
	writeTestFile(t, doctor, `{"codex":{"routePolicy":{"name":"conservative-quota-pressure"}},"repairPlan":[]}`)

	var out bytes.Buffer
	cmd := newProbeCmd()
	cmd.SetArgs([]string{"evidence", "--manifest", manifest, "--fail"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"ok: true",
		"manifest: manifest.json " + manifest + " status=passed stage=complete backend=native daemon=temporary",
		"route policy: conservative-quota-pressure ttl=1800s unknown=75.00 tie-break=0.01 windows=primary/secondary/code_review",
		"route candidates: 1 fresh=1",
		"quota fresh: true",
		"repair plan: 0 steps",
		"selftest status      true  passed",
		"route policy         true  conservative-quota-pressure",
		"quota freshness      true  quotaFresh=true",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q: %s", want, text)
		}
	}
}

func TestProbeEvidenceCmdJSONAndFail(t *testing.T) {
	dir := t.TempDir()
	summary := filepath.Join(dir, "summary.json")
	route := filepath.Join(dir, "agents-route.json")
	writeTestFile(t, summary, `{"summaryVersion":1,"status":"failed","stage":"live-codex-preflight","backend":"native","daemonMode":"existing","routeEvidencePath":"`+route+`"}`)
	writeTestFile(t, route, `{"routeCandidates":[{"accountId":"codex-a","quotaState":"stale","fresh":false}]}`)

	var out bytes.Buffer
	cmd := newProbeCmd()
	cmd.SetArgs([]string{"evidence", "--manifest", summary, "--artifact", route, "--json", "--fail"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "probe evidence failed") {
		t.Fatalf("err = %v output=%s", err, out.String())
	}
	text := out.String()
	for _, want := range []string{`"ok": false`, `"source": "summary.json"`, `"status": "failed"`, `"routeCandidates": 1`, `"freshCandidates": 0`, `"checks":`, `"name": "selftest status"`, `"name": "route policy"`, `"name": "quota freshness"`, `"routePolicy evidence missing"`, `"fresh quota evidence missing"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("json missing %q: %s", want, text)
		}
	}
}

func TestProbeEvidenceCmdWritesStandaloneHTMLReport(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	route := filepath.Join(dir, "agents-route.json")
	probe := filepath.Join(dir, "probe-data-readiness.json")
	report := filepath.Join(dir, "report", "evidence.html")
	writeTestFile(t, manifest, `{"manifestVersion":1,"status":"passed","stage":"complete","backend":"native","daemonMode":"existing","artifacts":{"agentsRoute":"agents-route.json","probeData":"probe-data-readiness.json"}}`)
	writeTestFile(t, route, `{"routePolicy":{"name":"conservative-quota-pressure","freshTtlSeconds":1800,"unknownScore":75,"currentAccountTieBreak":0.01,"quotaWindows":["primary"]},"routeCandidates":[{"accountId":"codex-a","quotaState":"fresh","fresh":true,"secretBackend":"native","primaryUsedPercent":12}],"access_token":"must-not-appear"}`)
	writeTestFile(t, probe, `{"summary":{"checkedAccounts":1,"freshQuotaAccounts":1,"staleQuotaAccounts":0,"missingQuotaAccounts":0,"autoRouteFresh":true},"repairPlan":[]}`)

	var out bytes.Buffer
	cmd := newProbeCmd()
	cmd.SetArgs([]string{"evidence", "--manifest", manifest, "--html", report, "--fail"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "html: "+report) {
		t.Fatalf("output missing html path: %s", out.String())
	}
	html, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	text := string(html)
	for _, want := range []string{
		"<title>capd evidence report</title>",
		"overall passed",
		"status passed",
		"backend native",
		"Route candidates",
		"conservative-quota-pressure",
		"quota freshness",
		"agents-route.json",
		"probe-data-readiness.json",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("html missing %q: %s", want, text)
		}
	}
	for _, forbidden := range []string{"must-not-appear", "access_token"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("html leaked raw artifact content %q: %s", forbidden, text)
		}
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
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
		w.Write([]byte(`{"ok":true,"summary":{"ready":true,"readiness":true,"checkedAccounts":2,"requiredAccounts":2,"missingAccounts":0,"freshQuotaAccounts":2,"staleQuotaAccounts":0,"missingQuotaAccounts":0,"autoRouteAccountId":"codex-a","autoRouteFresh":true,"routeDecisionOk":true,"routeCandidates":2,"secretBackend":"native","requiredSecretBackend":"native","secretBackendOk":true},"health":{"version":"test","protocolVersion":"0.1","secretBackend":"native"},"accountsCheck":{"provider":"codex","secretBackend":"native","checkedAccounts":2},"autoRoute":{"accountId":"codex-a","secretBackend":"native","quotaState":"fresh","fresh":true},"routePolicy":{"name":"conservative-quota-pressure","freshTtlSeconds":1800,"unknownScore":75,"currentAccountTieBreak":0.01,"quotaWindows":["primary","secondary","code_review"]},"checks":[{"name":"daemon health","ok":true,"evidence":"health ok"}]}`))
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
	for _, want := range []string{"summary: ready=true accounts=2/2 missing=0 quota fresh=2 stale=0 missing=0 autoFresh=true routeDecision=true routeCandidates=2 secretOK=true", "secret backend: actual=native required=native ok=true", "auto route: codex-a fresh fresh=true secret=native", "route policy: conservative-quota-pressure ttl=1800s unknown=75.00 tie-break=0.01 windows=primary/secondary/code_review"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q: %s", want, text)
		}
	}
}

func TestProbeDataTextPrintsPromptFreeMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeTokenForTest(home, "tok-probe-prompt-free"); err != nil {
		t.Fatal(err)
	}
	var rawQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"promptFree":true,"summary":{"ready":true,"readiness":false,"checkedAccounts":1,"requiredAccounts":2,"missingAccounts":1,"freshQuotaAccounts":1,"staleQuotaAccounts":0,"missingQuotaAccounts":0,"autoRouteAccountId":"codex-a","autoRouteFresh":true,"routeDecisionOk":true,"routeCandidates":1,"secretBackend":"native","secretBackendOk":true},"health":{"version":"test","protocolVersion":"0.1","secretBackend":"native"},"accountsCheck":{"provider":"codex","secretBackend":"native","checkedAccounts":1},"autoRoute":{"accountId":"codex-a","secretBackend":"native","quotaState":"fresh","fresh":true},"checks":[{"name":"account metadata","ok":true,"evidence":"1, need 1, secret native, secretState unknown"},{"name":"account credentials","ok":true,"evidence":"not checked in prompt-free probe"},{"name":"account runtime","ok":true,"evidence":"not checked in prompt-free probe"}]}`))
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
	if rawQuery != "" {
		t.Fatalf("query = %q", rawQuery)
	}
	text := out.String()
	for _, want := range []string{
		"mode: prompt-free account metadata (SecretStore and runtime not checked)",
		"account metadata: 1 checked, secret native",
		"account credentials  true  not checked in prompt-free probe",
		"account runtime      true  not checked in prompt-free probe",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "tok-probe-prompt-free") {
		t.Fatalf("output leaked token: %s", text)
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
		w.Write([]byte(`{"ok":false,"summary":{"ready":false,"readiness":true,"checkedAccounts":2,"requiredAccounts":2,"missingAccounts":0,"freshQuotaAccounts":0,"staleQuotaAccounts":1,"missingQuotaAccounts":1,"autoRouteAccountId":"codex-a","autoRouteFresh":false,"routeDecisionOk":false,"routeCandidates":2,"secretBackend":"native","requiredSecretBackend":"native","secretBackendOk":true},"health":{"version":"test","protocolVersion":"0.1","secretBackend":"native"},"autoRoute":{"accountId":"codex-a","secretBackend":"native","quotaState":"stale","fresh":false,"primaryUsedPercent":34.5},"routePolicy":{"name":"conservative-quota-pressure","freshTtlSeconds":1800,"unknownScore":75,"currentAccountTieBreak":0.01,"quotaWindows":["primary","secondary","code_review"]},"routeCandidates":[{"accountId":"codex-a","secretBackend":"native","quotaState":"stale","fresh":false,"primaryUsedPercent":34.5,"score":50,"reason":"stale quota"},{"accountId":"codex-b","secretBackend":"file","quotaState":"missing","fresh":false,"score":50,"reason":"missing quota"}],"checks":[{"name":"Codex auto route freshness","ok":false,"evidence":"codex-a stale fresh=false","nextStep":"refresh quota"}],"nextSteps":["refresh quota"],"repairPlan":[{"id":"refresh-quota-readiness","title":"Refresh quota and verify daemon-side readiness","command":"capd accounts check --json --readiness --require-secret-backend native --timeout 2m","expectedEvidence":"probe summary shows quotaRefreshed=true and autoRouteFresh=true","requiresDaemon":true,"requiresSecret":true}],"errors":[{"source":"agents/route","code":-32602,"message":"auto account codex-a without fresh cached quota"}]}`))
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
		"auto route: codex-a stale fresh=false secret=native",
		"route policy: conservative-quota-pressure ttl=1800s unknown=75.00 tie-break=0.01 windows=primary/secondary/code_review",
		"route candidates: codex-a stale fresh=false secret=native primary=34.5% stale quota; codex-b missing fresh=false secret=file missing quota",
		"error: agents/route code=-32602 auto account codex-a without fresh cached quota",
		"next: refresh quota",
		"repair plan:",
		"command: capd accounts check --json --readiness --require-secret-backend native --timeout 2m",
		"expect: probe summary shows quotaRefreshed=true and autoRouteFresh=true",
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
