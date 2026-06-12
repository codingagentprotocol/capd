package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/daemon"
)

func TestCodexAccountsSmokeProjectsWithoutLeakingSecrets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, secrets := seedCodexAccount(t)
	defer accounts.Close()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 12.5}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "codex-test") || !strings.Contains(text, "12.5%") {
		t.Fatalf("output = %s", text)
	}
	if !strings.Contains(text, "secret backend: file") {
		t.Fatalf("output missing secret backend: %s", text)
	}
	if strings.Contains(text, "access-secret") || strings.Contains(text, "refresh-secret") {
		t.Fatalf("smoke output leaked secret: %s", text)
	}
	home, err := daemon.Home()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyProjectedAuth(filepath.Join(home, "runtimes", codexauth.Provider, "codex-test")); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Get(context.Background(), secret.Ref{Backend: secret.BackendFile, ID: "codex-test"}); err != nil {
		t.Fatal(err)
	}
}

func TestCodexAccountsListShowsZeroQuotaWithoutLeakingSecrets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 0}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "list"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "0.0%") {
		t.Fatalf("output missing zero quota: %s", text)
	}
	if strings.Contains(text, "access-secret") || strings.Contains(text, "refresh-secret") || strings.Contains(text, "secretRef") {
		t.Fatalf("list output leaked secret: %s", text)
	}
}

func TestCodexAccountsQuotaPrintsSafeSummaryByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"planType":   "pro",
			"debugToken": "backend-secret",
			"rateLimits": map[string]any{
				"primary": map[string]any{"usedPercent": 37, "resetsAt": "2026-06-12T10:00:00Z"},
			},
		})
	}))
	defer srv.Close()

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "quota", "--base-url", srv.URL})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, secret := range []string{"backend-secret", "debugToken", "access-secret", "refresh-secret", "rawJson", "RawJSON"} {
		if strings.Contains(text, secret) {
			t.Fatalf("quota summary leaked %q: %s", secret, text)
		}
	}
	var result codexQuotaSummary
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != "codex-test" || result.Provider != codexauth.Provider || result.Plan != "pro" || result.PrimaryUsedPercent != 37 {
		t.Fatalf("result = %+v", result)
	}
	if q, err := accounts.LoadQuota("codex-test"); err != nil || q.PrimaryUsedPercent != 37 {
		t.Fatalf("cached quota = %+v err=%v", q, err)
	}
}

func TestCodexAccountsQuotaRawFlagPrintsBackendUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"planType":   "pro",
			"debugToken": "backend-debug-value",
			"rateLimits": map[string]any{
				"primary": map[string]any{"usedPercent": 11},
			},
		})
	}))
	defer srv.Close()

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "quota", "--base-url", srv.URL, "--raw"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "debugToken") || !strings.Contains(text, "backend-debug-value") {
		t.Fatalf("raw output missing backend JSON: %s", text)
	}
}

func TestAccountsListShowsAllProvidersWithoutLeakingSecrets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	if err := accounts.UpsertAccount(account.Account{
		ID:        "gemini-test",
		Provider:  "gemini",
		AuthMode:  "oauth",
		Email:     "gemini@example.com",
		AccountID: "gemini_remote",
		SecretRef: "file:gemini-secret",
	}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SetCurrentAccount("gemini", "gemini-test"); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 0}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"list"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"PROVIDER", "codex-test", "gemini-test", "gemini@example.com", "0.0%"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q: %s", want, text)
		}
	}
	for _, secret := range []string{"access-secret", "refresh-secret", "secretRef", "gemini-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("accounts list leaked %q: %s", secret, text)
		}
	}
}

func TestAccountsListJSONShowsAllProvidersWithoutLeakingSecrets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	if err := accounts.UpsertAccount(account.Account{
		ID:        "gemini-test",
		Provider:  "gemini",
		AuthMode:  "oauth",
		Email:     "gemini@example.com",
		SecretRef: "file:gemini-secret",
	}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"list", "--json"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, secret := range []string{"access-secret", "refresh-secret", "secretRef", "secret_ref", "gemini-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("accounts list json leaked %q: %s", secret, text)
		}
	}
	var rows []accountListRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Provider != codexauth.Provider || rows[0].ID != "codex-test" || !rows[0].Current {
		t.Fatalf("first row = %+v", rows[0])
	}
	if rows[1].Provider != "gemini" || rows[1].ID != "gemini-test" {
		t.Fatalf("second row = %+v", rows[1])
	}
}

func TestCodexAccountsSmokeJSONWithoutLeakingSecrets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 0}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke", "--json"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Contains(text, "access-secret") || strings.Contains(text, "refresh-secret") || strings.Contains(text, "secretRef") {
		t.Fatalf("smoke json leaked sensitive data: %s", text)
	}
	var result codexSmokeResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.CheckedAccounts != 1 || result.SecretBackend != secret.BackendFile || len(result.Accounts) != 1 {
		t.Fatalf("result = %+v", result)
	}
	acc := result.Accounts[0]
	if acc.ID != "codex-test" || !acc.ProjectionOK || !acc.RuntimeEnvOK || !acc.AuthJSONPrivate || !acc.ProjectionMarkerOK || acc.PrimaryUsed != "0.0%" || acc.PrimaryUsedPercent == nil || *acc.PrimaryUsedPercent != 0 {
		t.Fatalf("account = %+v", acc)
	}
	if result.AutoRoute == nil || result.AutoRoute.AccountID != "codex-test" {
		t.Fatalf("auto route = %+v", result.AutoRoute)
	}
}

func TestCodexAccountsSmokeJSONIncludesAutoRouteEvidence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, secrets := seedCodexAccount(t)
	defer accounts.Close()
	ref, err := secrets.Put(context.Background(), "codex-low", secret.Bundle{
		Provider:     codexauth.Provider,
		AuthMode:     "chatgpt",
		AccessToken:  "low-access-secret",
		RefreshToken: "low-refresh-secret",
		RawAuthJSON:  []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"low-access-secret","refresh_token":"low-refresh-secret"},"last_refresh":"2026-06-01T00:00:00Z"}`),
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
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 72}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-low", Plan: "pro", PrimaryUsedPercent: 4}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke", "--json", "--require-multiple"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, secret := range []string{"access-secret", "refresh-secret", "low-access-secret", "low-refresh-secret", "secretRef"} {
		if strings.Contains(text, secret) {
			t.Fatalf("smoke json leaked sensitive data: %s", text)
		}
	}
	var result codexSmokeResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.CheckedAccounts != 2 || result.SecretBackend != secret.BackendFile || result.AutoRoute == nil {
		t.Fatalf("result = %+v", result)
	}
	for _, acc := range result.Accounts {
		if !acc.ProjectionOK || !acc.RuntimeEnvOK || !acc.AuthJSONPrivate || !acc.ProjectionMarkerOK {
			t.Fatalf("projection evidence missing: %+v", acc)
		}
	}
	if result.AutoRoute.AccountID != "codex-low" || !result.AutoRoute.Fresh || result.AutoRoute.Primary == nil || *result.AutoRoute.Primary != 4 {
		t.Fatalf("auto route = %+v", result.AutoRoute)
	}
	if !strings.Contains(result.AutoRoute.Reason, "lowest fresh cached primary quota") {
		t.Fatalf("auto route reason = %q", result.AutoRoute.Reason)
	}
}

func TestCodexAccountsSmokeFailsWithoutAccounts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "no imported Codex accounts") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveUsageAccountAuto(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	if err := accounts.UpsertAccount(account.Account{
		ID:        "codex-low",
		Provider:  codexauth.Provider,
		AuthMode:  "chatgpt",
		Email:     "low@example.com",
		SecretRef: "file:codex-low",
	}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", PrimaryUsedPercent: 80}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-low", PrimaryUsedPercent: 5}); err != nil {
		t.Fatal(err)
	}
	acc, err := resolveUsageAccount(accounts, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if acc.ID != "codex-low" {
		t.Fatalf("account = %+v", acc)
	}
}

func TestResolveUsageAccountAutoIgnoresStaleQuota(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	if err := accounts.UpsertAccount(account.Account{
		ID:        "codex-fresh",
		Provider:  codexauth.Provider,
		AuthMode:  "chatgpt",
		Email:     "fresh@example.com",
		SecretRef: "file:codex-fresh",
	}); err != nil {
		t.Fatal(err)
	}
	staleAt := time.Now().Add(-account.QuotaRouteCacheTTL - time.Minute).Unix()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", PrimaryUsedPercent: 1, CheckedAt: staleAt}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-fresh", PrimaryUsedPercent: 20}); err != nil {
		t.Fatal(err)
	}
	acc, err := resolveUsageAccount(accounts, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if acc.ID != "codex-fresh" {
		t.Fatalf("account = %+v", acc)
	}
}

func TestResolveUsageAccountAutoNoAccounts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home, err := daemon.Home()
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := account.OpenStore(filepath.Join(home, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer accounts.Close()
	_, err = resolveUsageAccount(accounts, "auto")
	if err == nil || !strings.Contains(err.Error(), "no imported Codex accounts") {
		t.Fatalf("err = %v", err)
	}
}

func seedCodexAccount(t *testing.T) (*account.Store, secret.Store) {
	t.Helper()
	home, err := daemon.Home()
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := account.OpenStore(filepath.Join(home, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	secrets := secret.NewFileStore(filepath.Join(home, "secrets", "codex"))
	ref, err := secrets.Put(context.Background(), "codex-test", secret.Bundle{
		Provider:     codexauth.Provider,
		AuthMode:     "chatgpt",
		AccessToken:  "access-secret",
		RefreshToken: "refresh-secret",
		RawAuthJSON:  []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"access-secret","refresh_token":"refresh-secret"},"last_refresh":"2026-06-01T00:00:00Z"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.UpsertAccount(account.Account{
		ID:        "codex-test",
		Provider:  codexauth.Provider,
		AuthMode:  "chatgpt",
		Email:     "dev@example.com",
		AccountID: "acct_test",
		SecretRef: ref.String(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SetCurrentAccount(codexauth.Provider, "codex-test"); err != nil {
		t.Fatal(err)
	}
	return accounts, secrets
}
