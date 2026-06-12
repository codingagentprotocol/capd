package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
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
	for _, want := range []string{"capd doctor: needs attention", "daemon:", "codex:", "issues:", "next steps:"} {
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
	if report.Codex.CurrentAccountID != "codex-test" || report.Codex.AutoRouteAccountID != "codex-low" || !report.Codex.AutoRouteFresh {
		t.Fatalf("codex route summary = %+v", report.Codex)
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
