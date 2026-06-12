package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/server"
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
	if got.Summary.Ready || got.Summary.ImportedAccounts != 0 || got.Summary.RequiredAccounts != 2 || got.Summary.MissingAccounts != 2 || got.Summary.DaemonHealthy || got.Summary.SecretBackendOK != true {
		t.Fatalf("summary = %+v", got.Summary)
	}
	if len(got.Checks) == 0 {
		t.Fatalf("missing readiness checks: %+v", got)
	}
	for _, want := range []doctorCheckReport{
		{Name: "daemon health", OK: false, Evidence: "daemon /healthz failed", NextStep: "start the daemon with: capd start"},
		{Name: "Codex multi-account import", OK: false, Evidence: "imported 0 Codex account(s)", NextStep: "start the daemon with: capd start, then import through CAP with: capd accounts import (local fallback: capd accounts codex import)"},
		{Name: "Codex quota freshness", OK: false, Evidence: "fresh 0/0, stale 0, missing 0", NextStep: "start the daemon with: capd start, then import through CAP with: capd accounts import (local fallback: capd accounts codex import)"},
		{Name: "Codex auto route freshness", OK: false, Evidence: "auto route missing", NextStep: "start the daemon with: capd start, then import through CAP with: capd accounts import (local fallback: capd accounts codex import)"},
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
		"start the daemon with: capd start, then import through CAP with: capd accounts import",
		"local fallback: capd accounts codex import",
		"start the daemon with: capd start, then import a second Codex account through CAP with: capd accounts import --auth /path/to/auth.json, then run: make live-codex-preflight",
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
	for _, want := range []string{"capd doctor: needs attention", "daemon:", "codex:", "secretReadable=", "CHECK", "daemon health", "fail", "issues:", "next steps:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor text missing %q: %s", want, text)
		}
	}
}

func TestDoctorDaemonNextStepHonorsRequiredSecretBackend(t *testing.T) {
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
	want := "start the daemon with: capd start --secret-backend native"
	if !containsString(got.NextSteps, want) {
		t.Fatalf("next steps missing backend-specific daemon start: %+v", got.NextSteps)
	}
	if !containsDoctorCheck(got.Checks, doctorCheckReport{Name: "daemon health", OK: false, Evidence: "daemon /healthz failed", NextStep: want}) {
		t.Fatalf("daemon health check missing backend-specific next step: %+v", got.Checks)
	}
	if strings.Contains(out.String(), "start the daemon with: capd start\"") {
		t.Fatalf("doctor JSON kept generic daemon start step: %s", out.String())
	}
	if got := doctorReadinessNextStep(false, "native"); got != "start the daemon with: capd start --secret-backend native, then run: capd accounts check --json --readiness" {
		t.Fatalf("readiness next step = %q", got)
	}
	if got := doctorRouteReadinessNextStep(false, "native"); got != "start the daemon with: capd start --secret-backend native, then run: capd accounts check --json --readiness" {
		t.Fatalf("route next step = %q", got)
	}
	if got := doctorReadinessNextStep(true, "native"); got != "refresh and verify daemon-side readiness with: capd accounts check --json --readiness" {
		t.Fatalf("daemon-ready readiness next step = %q", got)
	}
}

func TestDoctorHelpIncludesTimeout(t *testing.T) {
	var out bytes.Buffer
	cmd := newDoctorCmd()
	cmd.SetArgs([]string{"--help"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "--timeout") || !strings.Contains(text, "2m") {
		t.Fatalf("doctor help missing timeout: %s", text)
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

func TestDoctorVerifySecretStoreRoundTripWithoutLeakingDiagnosticSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", "1")
	var out bytes.Buffer
	cmd := newDoctorCmd()
	cmd.SetArgs([]string{"--json", "--verify-secretstore"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got doctorReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !containsDoctorCheck(got.Checks, doctorCheckReport{
		Name:     "SecretStore roundtrip",
		OK:       true,
		Evidence: "roundtrip ok for backend file",
	}) {
		t.Fatalf("missing secretstore roundtrip check: %+v", got.Checks)
	}
	if got.Summary.SecretStoreRoundTripOK == nil || !*got.Summary.SecretStoreRoundTripOK {
		t.Fatalf("roundtrip summary = %+v", got.Summary)
	}
	for _, leaked := range []string{"doctor-secretstore-check", "capd-doctor", home} {
		if strings.Contains(out.String(), leaked) {
			t.Fatalf("doctor secretstore check leaked %q: %s", leaked, out.String())
		}
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
		"import a second Codex account through CAP with: capd accounts import --auth /path/to/auth.json, then run: make live-codex-preflight",
	} {
		if !containsString(report.NextSteps, want) {
			t.Fatalf("missing next step %q: %+v", want, report.NextSteps)
		}
	}
	if containsString(report.NextSteps, "import a Codex account with: capd accounts codex import") {
		t.Fatalf("old local-only next step should not be used when daemon is healthy: %+v", report.NextSteps)
	}
	if report.Codex.DaemonCheckError != "daemon token unavailable" {
		t.Fatalf("daemon check error = %q", report.Codex.DaemonCheckError)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), home) {
		t.Fatalf("doctor daemon check leaked home path: %s", data)
	}
}

func TestDoctorChecksDaemonAccountsThroughCAP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-doctor-cap"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	accounts, secrets := seedCodexAccount(t)
	defer accounts.Close()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 9}); err != nil {
		t.Fatal(err)
	}
	port := freeTCPPort(t)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", fmt.Sprint(port))
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	s := server.New(server.Options{
		Host: "127.0.0.1", Port: port, Token: token, Version: "it",
		Accounts: accounts, Secrets: secrets, RuntimeRoot: filepath.Join(home, "runtimes"),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	go func() { errCh <- s.Run(ctx) }()
	waitForHealthz(t, port)
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("server shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("server did not shut down")
		}
	})

	report, err := buildDoctorReport(t.Context(), doctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Codex.DaemonCheckOK || report.Codex.DaemonCheckedAccounts != 1 || report.Codex.DaemonSecretBackend != "file" || report.Codex.DaemonCheckError != "" {
		t.Fatalf("daemon check = %+v", report.Codex)
	}
	if !containsDoctorCheck(report.Checks, doctorCheckReport{
		Name:     "CAP accounts/check",
		OK:       true,
		Evidence: "checked 1 via daemon, secret backend file",
	}) {
		t.Fatalf("missing CAP accounts/check: %+v", report.Checks)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{token, "access-secret", "refresh-secret", "secretRef", filepath.Join(home, "runtimes")} {
		if strings.Contains(string(data), leaked) {
			t.Fatalf("doctor CAP report leaked %q: %s", leaked, data)
		}
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

	accounts, secrets := seedCodexAccount(t)
	defer accounts.Close()
	ref, err := secrets.Put(context.Background(), "codex-low", secret.Bundle{
		Provider:    codexauth.Provider,
		AuthMode:    "chatgpt",
		AccessToken: "low-access-secret",
		AccountID:   "acct_low",
		Email:       "low@example.com",
		RawAuthJSON: []byte(`{"tokens":{"access_token":"low-access-secret","account_id":"acct_low"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.UpsertAccount(account.Account{
		ID:        "codex-low",
		Provider:  codexauth.Provider,
		AuthMode:  "chatgpt",
		Email:     "low@example.com",
		AccountID: "acct_low",
		SecretRef: ref.String(),
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
	if report.Summary.ImportedAccounts != 2 || report.Summary.RequiredAccounts != 2 || report.Summary.MissingAccounts != 0 || report.Summary.FreshQuotaAccounts != 2 || report.Summary.StaleQuotaAccounts != 0 || report.Summary.MissingQuotaAccounts != 0 {
		t.Fatalf("summary quota = %+v", report.Summary)
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
	if report.Summary.AutoRouteAccountID != "codex-low" || !report.Summary.AutoRouteFresh || report.Summary.RouteCandidates != 2 || !report.Summary.DaemonHealthy || report.Summary.DaemonAccountsCheckOK {
		t.Fatalf("summary route/daemon = %+v", report.Summary)
	}
	if report.Codex.AutoRouteScore != 5 || report.Codex.AutoRoutePrimary == nil || *report.Codex.AutoRoutePrimary != 5 || report.Codex.AutoRouteCheckedAt == 0 {
		t.Fatalf("codex route evidence = %+v", report.Codex)
	}
	if report.Codex.AutoRouteReason != "auto account codex-low primary 5%" {
		t.Fatalf("codex route reason = %q", report.Codex.AutoRouteReason)
	}
	if len(report.Codex.RouteCandidates) != 2 {
		t.Fatalf("route candidates = %+v", report.Codex.RouteCandidates)
	}
	if report.Codex.RouteCandidates[0].AccountID != "codex-low" || !report.Codex.RouteCandidates[0].Fresh || report.Codex.RouteCandidates[0].PrimaryUsedPercent == nil || *report.Codex.RouteCandidates[0].PrimaryUsedPercent != 5 {
		t.Fatalf("first route candidate = %+v", report.Codex.RouteCandidates[0])
	}
	if report.Codex.RouteCandidates[1].AccountID != "codex-test" || !report.Codex.RouteCandidates[1].Fresh || report.Codex.RouteCandidates[1].PrimaryUsedPercent == nil || *report.Codex.RouteCandidates[1].PrimaryUsedPercent != 80 {
		t.Fatalf("second route candidate = %+v", report.Codex.RouteCandidates[1])
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"secretRef", "file:codex", "access-secret", "refresh-secret", "low-access-secret"} {
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
	if report.Codex.DaemonCheckError == "" || !containsString(report.Issues, "daemon-side accounts/check failed") {
		t.Fatalf("missing daemon-side check issue: %+v", report)
	}
	if !containsDoctorCheck(report.Checks, doctorCheckReport{
		Name:     "CAP accounts/check",
		OK:       false,
		Evidence: report.Codex.DaemonCheckError,
		NextStep: "inspect daemon-side account evidence with: capd accounts check --json --readiness",
	}) {
		t.Fatalf("missing CAP accounts/check failure: %+v", report.Checks)
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
	if len(report.Codex.RouteCandidates) != 3 {
		t.Fatalf("route candidates = %+v", report.Codex.RouteCandidates)
	}
	if report.Codex.RouteCandidates[0].AccountID != "codex-fresh" || !report.Codex.RouteCandidates[0].Fresh {
		t.Fatalf("first route candidate = %+v", report.Codex.RouteCandidates[0])
	}
	if report.Codex.RouteCandidates[1].AccountID != "codex-test" || report.Codex.RouteCandidates[1].QuotaState != protocol.AccountQuotaStateStale {
		t.Fatalf("second route candidate = %+v", report.Codex.RouteCandidates[1])
	}
	if report.Codex.RouteCandidates[2].AccountID != "codex-missing" || report.Codex.RouteCandidates[2].QuotaState != protocol.AccountQuotaStateMissing {
		t.Fatalf("third route candidate = %+v", report.Codex.RouteCandidates[2])
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

func TestDoctorReportsUnreadableAccountSecretsSafely(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", "1")
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	acc, err := accounts.LoadAccount("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	acc.SecretRef = secret.BackendNative + ":codex-test"
	if err := accounts.UpsertAccount(acc); err != nil {
		t.Fatal(err)
	}

	report, err := buildDoctorReport(t.Context(), doctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Codex.SecretReadableAccounts != 0 || report.Codex.SecretUnreadableAccounts != 1 {
		t.Fatalf("secret readability = %+v", report.Codex)
	}
	if len(report.Codex.Accounts) != 1 || report.Codex.Accounts[0].SecretState != "backend-mismatch" || report.Codex.Accounts[0].SecretBackendOK || report.Codex.Accounts[0].SecretReadable {
		t.Fatalf("account secret state = %+v", report.Codex.Accounts)
	}
	if report.Summary.SecretReadableAccounts != 0 || report.Summary.SecretUnreadableAccounts != 1 {
		t.Fatalf("summary secret readability = %+v", report.Summary)
	}
	if !containsString(report.Issues, "not every imported Codex account has readable SecretStore credentials") {
		t.Fatalf("missing secret readability issue: %+v", report.Issues)
	}
	if !containsDoctorCheck(report.Checks, doctorCheckReport{
		Name:     "Codex SecretStore credentials",
		OK:       false,
		Evidence: "readable 0/1, unreadable 1 (backend-mismatch 1)",
		NextStep: "restart capd with the active SecretStore backend, then re-import affected Codex accounts",
	}) {
		t.Fatalf("missing secret readability check: %+v", report.Checks)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"secretRef", "native:codex-test", "access-secret", "refresh-secret", home} {
		if strings.Contains(string(data), leaked) {
			t.Fatalf("doctor secret readability leaked %q: %s", leaked, data)
		}
	}
}

func TestDoctorReportsMalformedSecretRefsSafely(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", "1")
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	acc, err := accounts.LoadAccount("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	acc.SecretRef = "file:"
	if err := accounts.UpsertAccount(acc); err != nil {
		t.Fatal(err)
	}

	report, err := buildDoctorReport(t.Context(), doctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Codex.Accounts) != 1 || report.Codex.Accounts[0].SecretState != "malformed-ref" || report.Codex.Accounts[0].SecretReadable {
		t.Fatalf("account secret state = %+v", report.Codex.Accounts)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"secretRef", "file:", "access-secret", "refresh-secret", home} {
		if strings.Contains(string(data), leaked) {
			t.Fatalf("doctor malformed secret state leaked %q: %s", leaked, data)
		}
	}
}

func TestDoctorSecretReadinessNextStepUsesSecretState(t *testing.T) {
	if got := doctorSecretReadinessNextStep(false, map[string]int{doctorSecretStateTimeout: 1}); !strings.Contains(got, "unlock or approve OS SecretStore access") || !strings.Contains(got, "--timeout 2m") {
		t.Fatalf("timeout next step = %q", got)
	}
	if got := doctorSecretErrorState(errors.New("load account secret: macOS keychain status -128")); got != doctorSecretStateAccessDenied {
		t.Fatalf("access denied state = %q", got)
	}
	if got := doctorSecretReadinessNextStep(true, map[string]int{doctorSecretStateAccessDenied: 1}); !strings.Contains(got, "macOS Keychain access") || !strings.Contains(got, "capd start --secret-backend file") {
		t.Fatalf("access denied next step = %q", got)
	}
	if got := doctorSecretReadinessEvidence(0, 2, 2, map[string]int{doctorSecretStateTimeout: 1, doctorSecretStateMissing: 1}); got != "readable 0/2, unreadable 2 (timeout 1, missing 1)" {
		t.Fatalf("evidence = %q", got)
	}
	if got := doctorSecretReadinessNextStep(true, map[string]int{doctorSecretStateMissing: 1}); !strings.Contains(got, "re-import missing Codex credentials through CAP") {
		t.Fatalf("missing next step = %q", got)
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
