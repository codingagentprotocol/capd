package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestDoctorJSONReportsMissingReadinessWithoutSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", "1")
	if err := writeTokenForTest(home, "tok-doctor-secret"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := newDoctorCmd()
	cmd.SetArgs([]string{"--json"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got doctorReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK {
		t.Fatalf("doctor unexpectedly ok: %+v", got)
	}
	if got.Daemon.OK || got.Codex.ImportedAccounts != 0 {
		t.Fatalf("report = %+v", got)
	}
	if len(got.Checks) == 0 {
		t.Fatalf("missing readiness checks: %+v", got)
	}
	for _, want := range []doctorCheckReport{
		{Name: "daemon health", OK: false, Evidence: "daemon /healthz failed", NextStep: "start the daemon with: capd start"},
		{Name: "Codex multi-account import", OK: false, Evidence: "imported 0 Codex account(s)", NextStep: "after starting the daemon, import through CAP with: capd accounts import (local fallback: capd accounts codex import)"},
		{Name: "Codex quota freshness", OK: false, Evidence: "fresh 0/0, stale 0, missing 0", NextStep: "after starting the daemon, import through CAP with: capd accounts import (local fallback: capd accounts codex import)"},
		{Name: "Codex auto route freshness", OK: false, Evidence: "auto route missing", NextStep: "after starting the daemon, import through CAP with: capd accounts import (local fallback: capd accounts codex import)"},
	} {
		if !containsDoctorCheck(got.Checks, want) {
			t.Fatalf("missing check %+v in %+v", want, got.Checks)
		}
	}
	body := out.String()
	for _, leaked := range []string{"tok-doctor-secret", home} {
		if strings.Contains(body, leaked) {
			t.Fatalf("doctor JSON leaked %q: %s", leaked, body)
		}
	}
	for _, want := range []string{"daemon health check failed", "no imported Codex accounts", "multi-account readiness requires at least two imported Codex accounts"} {
		if !strings.Contains(body, want) {
			t.Fatalf("doctor JSON missing %q: %s", want, body)
		}
	}
	for _, want := range []string{
		"after starting the daemon, import through CAP with: capd accounts import",
		"local fallback: capd accounts codex import",
		"start the daemon, import a second Codex account, then run: make live-codex-readiness",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("doctor JSON missing next step %q: %s", want, body)
		}
	}
}

func TestDoctorTextReturnsErrorWhenNotReady(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", "1")
	var out bytes.Buffer
	cmd := newDoctorCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "readiness issue") {
		t.Fatalf("err = %v", err)
	}
	text := out.String()
	for _, want := range []string{"capd doctor: needs attention", "daemon:", "codex:", "CHECK", "daemon health", "fail", "issues:", "next steps:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor text missing %q: %s", want, text)
		}
	}
}

func TestDoctorJSONFailReturnsErrorAfterWritingReport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", "1")
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd := newDoctorCmd()
	cmd.SetArgs([]string{"--json", "--fail"})
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "readiness issue") {
		t.Fatalf("err = %v", err)
	}
	var got doctorReport
	if err := json.NewDecoder(bytes.NewReader(out.Bytes())).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.OK || !containsString(got.Issues, "daemon health check failed") {
		t.Fatalf("report = %+v", got)
	}
}

func TestDoctorRequiresSecretBackend(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", "1")
	var out bytes.Buffer
	cmd := newDoctorCmd()
	cmd.SetArgs([]string{"--json", "--require-secret-backend", "native"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got doctorReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK {
		t.Fatalf("doctor unexpectedly ok: %+v", got)
	}
	if !containsString(got.Issues, `secret backend is "file", want "native"`) {
		t.Fatalf("missing secret backend issue: %+v", got.Issues)
	}
	if strings.Contains(out.String(), home) {
		t.Fatalf("doctor JSON leaked home path: %s", out.String())
	}
}

func TestDoctorRecommendsDaemonImportWhenDaemonHealthy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Write([]byte("ok\n"))
	}))
	defer ts.Close()
	host, port := splitTestURL(t, ts.URL)
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)

	report, err := buildDoctorReport(t.Context(), doctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"import a Codex account through CAP with: capd accounts import",
		"import a second Codex account through CAP with: capd accounts import --auth /path/to/auth.json, then run: make live-codex-readiness",
	} {
		if !containsString(report.NextSteps, want) {
			t.Fatalf("missing next step %q: %+v", want, report.NextSteps)
		}
	}
	if containsString(report.NextSteps, "import a Codex account with: capd accounts codex import") {
		t.Fatalf("old local-only next step should not be used when daemon is healthy: %+v", report.NextSteps)
	}
}

func TestDoctorRejectsInvalidRequiredSecretBackend(t *testing.T) {
	cmd := newDoctorCmd()
	cmd.SetArgs([]string{"--require-secret-backend", "bogus"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "secret backend") {
		t.Fatalf("err = %v", err)
	}
}

func TestDoctorReportsMultiAccountQuotaAndAutoRoute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Write([]byte("ok\n"))
	}))
	defer ts.Close()
	host, port := splitTestURL(t, ts.URL)
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)

	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	if err := accounts.UpsertAccount(account.Account{
		ID:        "codex-low",
		Provider:  codexauth.Provider,
		AuthMode:  "chatgpt",
		Email:     "low@example.com",
		AccountID: "acct_low",
		SecretRef: "file:codex-low",
	}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 80}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-low", Plan: "pro", PrimaryUsedPercent: 5}); err != nil {
		t.Fatal(err)
	}

	report, err := buildDoctorReport(t.Context(), doctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Daemon.OK {
		t.Fatalf("daemon = %+v", report.Daemon)
	}
	if report.Codex.ImportedAccounts != 2 || report.Codex.FreshQuotaAccounts != 2 || report.Codex.StaleQuotaAccounts != 0 || report.Codex.MissingQuotaAccounts != 0 {
		t.Fatalf("codex quota summary = %+v", report.Codex)
	}
	if len(report.Codex.Accounts) != 2 {
		t.Fatalf("codex accounts = %+v", report.Codex.Accounts)
	}
	if report.Codex.Accounts[0].ID != "codex-low" || report.Codex.Accounts[0].QuotaState != protocol.AccountQuotaStateFresh || report.Codex.Accounts[0].PrimaryUsedPercent == nil || *report.Codex.Accounts[0].PrimaryUsedPercent != 5 {
		t.Fatalf("first account evidence = %+v", report.Codex.Accounts[0])
	}
	if report.Codex.Accounts[1].ID != "codex-test" || !report.Codex.Accounts[1].Current || report.Codex.Accounts[1].PrimaryUsedPercent == nil || *report.Codex.Accounts[1].PrimaryUsedPercent != 80 {
		t.Fatalf("second account evidence = %+v", report.Codex.Accounts[1])
	}
	if report.Codex.CurrentAccountID != "codex-test" || report.Codex.AutoRouteAccountID != "codex-low" || !report.Codex.AutoRouteFresh {
		t.Fatalf("codex route summary = %+v", report.Codex)
	}
	if report.Codex.AutoRouteScore != 5 || report.Codex.AutoRoutePrimary == nil || *report.Codex.AutoRoutePrimary != 5 || report.Codex.AutoRouteCheckedAt == 0 {
		t.Fatalf("codex route evidence = %+v", report.Codex)
	}
	if report.Codex.AutoRouteReason != "auto account codex-low primary 5%" {
		t.Fatalf("codex route reason = %q", report.Codex.AutoRouteReason)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"secretRef", "file:codex", "access-secret", "refresh-secret"} {
		if strings.Contains(string(data), leaked) {
			t.Fatalf("doctor report leaked %q: %s", leaked, data)
		}
	}
	for _, forbidden := range []string{
		"no imported Codex accounts",
		"multi-account readiness requires at least two imported Codex accounts",
		"not every imported Codex account has fresh quota evidence",
		"auto account route is not backed by fresh quota",
	} {
		if containsString(report.Issues, forbidden) {
			t.Fatalf("unexpected issue %q in %+v", forbidden, report.Issues)
		}
	}
	if report.Codex.CLIAvailable && !report.OK {
		t.Fatalf("doctor should be ok when CLI, daemon, accounts, quota, and route are ready: %+v", report.Issues)
	}
	if !report.Codex.CLIAvailable && !containsString(report.Issues, "Codex CLI is not available") {
		t.Fatalf("missing CLI issue: %+v", report.Issues)
	}
}

func TestDoctorReportsStaleAndMissingAccountQuota(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Write([]byte("ok\n"))
	}))
	defer ts.Close()
	host, port := splitTestURL(t, ts.URL)
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)

	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	for _, acc := range []account.Account{
		{ID: "codex-fresh", Provider: codexauth.Provider, AuthMode: "chatgpt", Email: "fresh@example.com", SecretRef: "file:codex-fresh"},
		{ID: "codex-missing", Provider: codexauth.Provider, AuthMode: "chatgpt", Email: "missing@example.com", SecretRef: "file:codex-missing"},
	} {
		if err := accounts.UpsertAccount(acc); err != nil {
			t.Fatal(err)
		}
	}
	staleAt := time.Now().Add(-account.QuotaRouteCacheTTL - time.Minute).Unix()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 1, CheckedAt: staleAt}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-fresh", Plan: "pro", PrimaryUsedPercent: 20}); err != nil {
		t.Fatal(err)
	}

	report, err := buildDoctorReport(t.Context(), doctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Codex.ImportedAccounts != 3 || report.Codex.FreshQuotaAccounts != 1 || report.Codex.StaleQuotaAccounts != 1 || report.Codex.MissingQuotaAccounts != 1 {
		t.Fatalf("codex quota summary = %+v", report.Codex)
	}
	if report.Codex.AutoRouteAccountID != "codex-fresh" || !report.Codex.AutoRouteFresh || report.Codex.AutoRouteScore != 20 {
		t.Fatalf("auto route = %+v", report.Codex)
	}
	byID := map[string]doctorCodexAccountReport{}
	for _, row := range report.Codex.Accounts {
		byID[row.ID] = row
	}
	if byID["codex-test"].QuotaState != protocol.AccountQuotaStateStale || byID["codex-test"].QuotaFresh || byID["codex-test"].QuotaCheckedAt != staleAt {
		t.Fatalf("stale account evidence = %+v", byID["codex-test"])
	}
	if byID["codex-missing"].QuotaState != protocol.AccountQuotaStateMissing || byID["codex-missing"].QuotaFresh || byID["codex-missing"].PrimaryUsedPercent != nil {
		t.Fatalf("missing account evidence = %+v", byID["codex-missing"])
	}
	if !containsString(report.Issues, "not every imported Codex account has fresh quota evidence") {
		t.Fatalf("missing quota freshness issue: %+v", report.Issues)
	}
	if !containsString(report.NextSteps, "refresh and verify daemon-side readiness with: capd accounts check --json --readiness") {
		t.Fatalf("missing readiness next step: %+v", report.NextSteps)
	}
	if !containsDoctorCheck(report.Checks, doctorCheckReport{
		Name:     "Codex quota freshness",
		OK:       false,
		Evidence: "fresh 1/3, stale 1, missing 1",
		NextStep: "refresh and verify daemon-side readiness with: capd accounts check --json --readiness",
	}) {
		t.Fatalf("missing quota freshness check: %+v", report.Checks)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsDoctorCheck(values []doctorCheckReport, want doctorCheckReport) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
