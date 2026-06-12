package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/daemon"
	"github.com/codingagentprotocol/capd/internal/server"
	"github.com/codingagentprotocol/capd/pkg/protocol"
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
	if !strings.Contains(text, "0.0%") || !strings.Contains(text, "QUOTA_STATE") || !strings.Contains(text, protocol.AccountQuotaStateFresh) {
		t.Fatalf("output missing zero quota: %s", text)
	}
	if strings.Contains(text, "access-secret") || strings.Contains(text, "refresh-secret") || strings.Contains(text, "secretRef") {
		t.Fatalf("list output leaked secret: %s", text)
	}
}

func TestCodexAccountsListJSONShowsQuotaWithoutLeakingSecrets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	checkedAt := time.Now().Unix()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 0, CheckedAt: checkedAt}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "list", "--json"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, secret := range []string{"access-secret", "refresh-secret", "secretRef", "secret_ref"} {
		if strings.Contains(text, secret) {
			t.Fatalf("codex list json leaked %q: %s", secret, text)
		}
	}
	var rows []accountListRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	row := rows[0]
	if !row.Current || row.Provider != codexauth.Provider || row.ID != "codex-test" || row.Plan != "pro" || row.PrimaryUsed != "0.0%" || row.QuotaState != protocol.AccountQuotaStateFresh || row.QuotaCheckedAt != checkedAt {
		t.Fatalf("row = %+v", row)
	}
}

func TestAccountsCheckHelpExplainsDaemonRequirement(t *testing.T) {
	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"check", "--help"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, needle := range []string{
		"running capd",
		"daemon",
		"capd start",
		"capd accounts codex smoke",
		"--readiness",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("help missing %q: %s", needle, text)
		}
	}
}

func TestCodexAccountsImportMissingAuthDoesNotLeakPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	missingPath := filepath.Join(t.TempDir(), "missing-auth.json")
	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "import", "--auth", missingPath})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected missing auth error")
	}
	if !strings.Contains(err.Error(), "read auth json failed") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), missingPath) {
		t.Fatalf("import error leaked path: %v", err)
	}
}

func TestCodexAccountsImportUsesAuthPathListEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first-auth.json")
	secondPath := filepath.Join(dir, "second-auth.json")
	if err := os.WriteFile(firstPath, []byte(`{
		"auth_mode": "chatgpt",
		"email": "first@example.com",
		"tokens": {
			"access_token": "first-access-secret",
			"refresh_token": "first-refresh-secret",
			"account_id": "acct_first"
		}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte(`{
		"auth_mode": "chatgpt",
		"email": "second@example.com",
		"tokens": {
			"access_token": "second-access-secret",
			"refresh_token": "second-refresh-secret",
			"account_id": "acct_second"
		}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPD_CODEX_AUTH_PATHS", strings.Join([]string{"", firstPath, secondPath}, string(os.PathListSeparator)))

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "import"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"imported codex-acct_first <first@example.com>", "imported codex-acct_second <second@example.com>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q: %s", want, text)
		}
	}
	for _, leaked := range []string{firstPath, secondPath, "first-access-secret", "first-refresh-secret", "second-access-secret", "second-refresh-secret", "secretRef", "secret_ref"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("import output leaked %q: %s", leaked, text)
		}
	}
	accounts, err := account.OpenStore(filepath.Join(home, ".capd", "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer accounts.Close()
	list, err := accounts.ListAccounts(codexauth.Provider)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]account.Account{}
	for _, acc := range list {
		byID[acc.ID] = acc
	}
	if len(list) != 2 || byID["codex-acct_first"].Email != "first@example.com" || byID["codex-acct_second"].Email != "second@example.com" {
		t.Fatalf("accounts = %+v", list)
	}
	current, err := accounts.CurrentAccount(codexauth.Provider)
	if err != nil {
		t.Fatal(err)
	}
	if current != "codex-acct_first" {
		t.Fatalf("current = %q", current)
	}
}

func TestCodexAccountsImportAcceptsRepeatedAuthFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first-auth.json")
	secondPath := filepath.Join(dir, "second-auth.json")
	envPath := filepath.Join(dir, "env-auth.json")
	for path, body := range map[string]string{
		firstPath:  `{"email":"first@example.com","tokens":{"access_token":"first-access-secret","account_id":"acct_first"}}`,
		secondPath: `{"email":"second@example.com","tokens":{"access_token":"second-access-secret","account_id":"acct_second"}}`,
		envPath:    `{"email":"env@example.com","tokens":{"access_token":"env-access-secret","account_id":"acct_env"}}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CAPD_CODEX_AUTH_PATHS", envPath)

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "import", "--auth", firstPath, "--auth", secondPath})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"imported codex-acct_first <first@example.com>", "imported codex-acct_second <second@example.com>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q: %s", want, text)
		}
	}
	for _, leaked := range []string{firstPath, secondPath, envPath, "first-access-secret", "second-access-secret", "env-access-secret"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("import output leaked %q: %s", leaked, text)
		}
	}
	accounts, err := account.OpenStore(filepath.Join(home, ".capd", "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer accounts.Close()
	list, err := accounts.ListAccounts(codexauth.Provider)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]account.Account{}
	for _, acc := range list {
		byID[acc.ID] = acc
	}
	if len(list) != 2 || byID["codex-acct_first"].Email != "first@example.com" || byID["codex-acct_second"].Email != "second@example.com" {
		t.Fatalf("accounts = %+v", list)
	}
	if _, ok := byID["codex-acct_env"]; ok {
		t.Fatalf("--auth should override CAPD_CODEX_AUTH_PATHS: %+v", list)
	}
}

func TestAccountsCheckCallsDaemonRPCWithoutLeakingSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok &with?chars"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	accounts, secrets := seedCodexAccount(t)
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 12}); err != nil {
		t.Fatal(err)
	}
	var quotaCalls atomic.Int32
	quotaBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		quotaCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer access-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "acct_test" {
			t.Fatalf("ChatGPT-Account-Id = %q", got)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"planType": "team",
			"rateLimits": map[string]any{
				"primary": map[string]any{"usedPercent": 6},
			},
			"debug": "backend-secret",
		})
	}))
	defer quotaBackend.Close()
	port := freeTCPPort(t)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", strconv.Itoa(port))
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	s := server.New(server.Options{
		Host: "127.0.0.1", Port: port, Token: token, Version: "it",
		Accounts: accounts, Secrets: secrets, RuntimeRoot: filepath.Join(home, "runtimes"),
		CodexQuotaBaseURL: quotaBackend.URL,
		Log:               slog.New(slog.NewTextHandler(io.Discard, nil)),
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
		accounts.Close()
	})

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"check", "--json"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result protocol.AccountsCheckResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Provider != codexauth.Provider || result.CurrentAccountID != "codex-test" || result.SecretBackend != secret.BackendFile || result.CheckedAccounts != 1 || len(result.Accounts) != 1 {
		t.Fatalf("result = %+v", result)
	}
	row := result.Accounts[0]
	if row.ID != "codex-test" || !row.Current || !row.SecretBackendOK || !row.CredentialReadable || !row.RuntimeReady || !row.AuthJSONPrivate || !row.ProjectionMarkerOK || row.QuotaState != protocol.AccountQuotaStateFresh || !row.QuotaFresh {
		t.Fatalf("row = %+v", row)
	}
	if result.AutoRoute == nil || result.AutoRoute.AccountID != "codex-test" || result.AutoRoute.QuotaState != protocol.AccountQuotaStateFresh {
		t.Fatalf("auto route = %+v", result.AutoRoute)
	}
	if len(result.RouteCandidates) != 1 || result.RouteCandidates[0].AccountID != "codex-test" || !result.RouteCandidates[0].Fresh {
		t.Fatalf("route candidates = %+v", result.RouteCandidates)
	}
	text := out.String()
	for _, leaked := range []string{token, "access-secret", "refresh-secret", "secretRef", "secret_ref", "CODEX_HOME", filepath.Join(home, "runtimes")} {
		if strings.Contains(text, leaked) {
			t.Fatalf("accounts check leaked %q: %s", leaked, text)
		}
	}

	out.Reset()
	cmd = newAccountsCmd()
	cmd.SetArgs([]string{"check"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "quota refreshed: false") {
		t.Fatalf("text output missing quota refresh evidence: %s", out.String())
	}
	for _, want := range []string{"auto route: codex-test quota fresh fresh true primary 12.0% score ", "FRESH", "PRIMARY", "CHECKED_AT", protocol.AccountQuotaStateFresh, "true", "12.0%"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("text output missing %q: %s", want, out.String())
		}
	}

	out.Reset()
	cmd = newAccountsCmd()
	cmd.SetArgs([]string{"check", "--json", "--require-fresh-quota", "--require-all-fresh-quota", "--require-secret-backend", secret.BackendFile})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if quotaCalls.Load() != 0 {
		t.Fatalf("quota calls before refresh = %d", quotaCalls.Load())
	}

	out.Reset()
	cmd = newAccountsCmd()
	cmd.SetArgs([]string{"check", "--json", "--refresh-quota", "--require-fresh-quota", "--require-all-fresh-quota"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if quotaCalls.Load() != 1 {
		t.Fatalf("quota calls = %d", quotaCalls.Load())
	}
	var refreshed protocol.AccountsCheckResult
	if err := json.Unmarshal(out.Bytes(), &refreshed); err != nil {
		t.Fatal(err)
	}
	if refreshed.AutoRoute == nil || !refreshed.AutoRoute.Fresh || refreshed.AutoRoute.PrimaryUsedPercent == nil || *refreshed.AutoRoute.PrimaryUsedPercent != 6 {
		t.Fatalf("refreshed auto route = %+v", refreshed.AutoRoute)
	}
	if len(refreshed.RouteCandidates) != 1 || refreshed.RouteCandidates[0].AccountID != "codex-test" || refreshed.RouteCandidates[0].PrimaryUsedPercent == nil || *refreshed.RouteCandidates[0].PrimaryUsedPercent != 6 {
		t.Fatalf("refreshed route candidates = %+v", refreshed.RouteCandidates)
	}
	if !refreshed.QuotaRefreshed {
		t.Fatalf("quotaRefreshed = false")
	}
	for _, leaked := range []string{token, "access-secret", "refresh-secret", "backend-secret", "secretRef", "secret_ref", "CODEX_HOME", filepath.Join(home, "runtimes")} {
		if strings.Contains(out.String(), leaked) {
			t.Fatalf("accounts check refresh leaked %q: %s", leaked, out.String())
		}
	}

	out.Reset()
	cmd = newAccountsCmd()
	cmd.SetArgs([]string{"check", "--refresh-quota"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if quotaCalls.Load() != 2 {
		t.Fatalf("quota calls after text refresh = %d", quotaCalls.Load())
	}
	if !strings.Contains(out.String(), "quota refreshed: true") {
		t.Fatalf("text output missing refreshed evidence: %s", out.String())
	}
	for _, want := range []string{"auto route: codex-test quota fresh fresh true primary 6.0% score ", "FRESH", "PRIMARY", "CHECKED_AT", protocol.AccountQuotaStateFresh, "true", "6.0%"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("text refresh output missing %q: %s", want, out.String())
		}
	}
	for _, leaked := range []string{token, "access-secret", "refresh-secret", "backend-secret", "secretRef", "secret_ref", "CODEX_HOME", filepath.Join(home, "runtimes")} {
		if strings.Contains(out.String(), leaked) {
			t.Fatalf("accounts check text refresh leaked %q: %s", leaked, out.String())
		}
	}

	out.Reset()
	cmd = newAccountsCmd()
	cmd.SetArgs([]string{"check", "--require-multiple"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "expected multiple Codex accounts") {
		t.Fatalf("err = %v", err)
	}

	out.Reset()
	cmd = newAccountsCmd()
	cmd.SetArgs([]string{"check", "--require-secret-backend", secret.BackendNative})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `want "native"`) {
		t.Fatalf("err = %v", err)
	}
	for _, leaked := range []string{token, "access-secret", "refresh-secret", "secretRef", "secret_ref", "CODEX_HOME", filepath.Join(home, "runtimes")} {
		if strings.Contains(err.Error(), leaked) || strings.Contains(out.String(), leaked) {
			t.Fatalf("accounts check gate leaked %q: err=%v out=%s", leaked, err, out.String())
		}
	}

	out.Reset()
	cmd = newAccountsCmd()
	cmd.SetArgs([]string{"check", "--json", "--require-secret-backend", secret.BackendNative})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `want "native"`) {
		t.Fatalf("json err = %v", err)
	}
	var failure accountsCheckJSONError
	if unmarshalErr := json.Unmarshal(out.Bytes(), &failure); unmarshalErr != nil {
		t.Fatalf("json error output = %q: %v", out.String(), unmarshalErr)
	}
	if failure.OK || failure.Error.Code != protocol.CodeInvalidParams || !strings.Contains(failure.Error.Message, `want "native"`) || len(failure.Data) == 0 {
		t.Fatalf("json failure = %+v", failure)
	}
	var partial protocol.AccountsCheckResult
	if unmarshalErr := json.Unmarshal(failure.Data, &partial); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if partial.CheckedAccounts != 1 || partial.SecretBackend != secret.BackendFile || len(partial.Accounts) != 0 {
		t.Fatalf("partial = %+v", partial)
	}
	for _, leaked := range []string{token, "access-secret", "refresh-secret", "secretRef", "secret_ref", "CODEX_HOME", filepath.Join(home, "runtimes")} {
		if strings.Contains(out.String(), leaked) {
			t.Fatalf("accounts check json gate leaked %q: %s", leaked, out.String())
		}
	}
}

func TestAccountsImportCallsDaemonRPCWithRepeatedAuthFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-import-daemon"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	accounts, secrets := seedCodexAccount(t)
	defer accounts.Close()
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first-auth.json")
	secondPath := filepath.Join(dir, "second-auth.json")
	for path, body := range map[string]string{
		firstPath:  `{"email":"first@example.com","tokens":{"access_token":"first-access-secret","refresh_token":"first-refresh-secret","account_id":"acct_first"}}`,
		secondPath: `{"email":"second@example.com","tokens":{"access_token":"second-access-secret","refresh_token":"second-refresh-secret","account_id":"acct_second"}}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	port := freeTCPPort(t)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", strconv.Itoa(port))
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

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"import", "--json", "--auth", firstPath, "--auth", secondPath})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result protocol.AccountsImportResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Accounts) != 2 || result.Accounts[0].ID != "codex-acct_first" || result.Accounts[1].ID != "codex-acct_second" || result.Account.ID != "codex-acct_second" || result.ImportedAccounts != 3 {
		t.Fatalf("result = %+v", result)
	}
	for _, id := range []string{"codex-acct_first", "codex-acct_second"} {
		if _, err := accounts.LoadAccount(id); err != nil {
			t.Fatal(err)
		}
	}
	for _, leaked := range []string{token, firstPath, secondPath, "first-access-secret", "second-access-secret", "first-refresh-secret", "second-refresh-secret", "secretRef", "secret_ref", "CODEX_HOME", filepath.Join(home, "runtimes")} {
		if strings.Contains(out.String(), leaked) {
			t.Fatalf("accounts import leaked %q: %s", leaked, out.String())
		}
	}

	out.Reset()
	cmd = newAccountsCmd()
	cmd.SetArgs([]string{"import", "--auth", firstPath})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "imported codex-acct_first <first@example.com>") || !strings.Contains(out.String(), "current codex-test") || !strings.Contains(out.String(), "next: verify readiness with: capd accounts check --readiness") {
		t.Fatalf("text output = %s", out.String())
	}
	if strings.Contains(out.String(), firstPath) || strings.Contains(out.String(), "first-access-secret") || strings.Contains(out.String(), token) {
		t.Fatalf("accounts import text leaked data: %s", out.String())
	}
}

func TestAccountsImportNextStep(t *testing.T) {
	cases := map[int]string{
		0: "",
		1: "import a second Codex account with: capd accounts import --auth /path/to/auth.json",
		2: "verify readiness with: capd accounts check --readiness",
		3: "verify readiness with: capd accounts check --readiness",
	}
	for imported, want := range cases {
		if got := accountsImportNextStep(imported); got != want {
			t.Fatalf("accountsImportNextStep(%d) = %q, want %q", imported, got, want)
		}
	}
}

func TestAccountsCheckReadinessShortcutSetsDaemonGateParams(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-readiness-shortcut"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	accounts, secrets := seedCodexAccount(t)
	defer accounts.Close()
	ref, err := secrets.Put(context.Background(), "codex-alt", secret.Bundle{
		Provider:    codexauth.Provider,
		AuthMode:    "oauth",
		AccessToken: "alt-access-secret",
		AccountID:   "acct_alt",
		Email:       "alt@example.com",
		RawAuthJSON: []byte(`{"tokens":{"access_token":"alt-access-secret"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.UpsertAccount(account.Account{
		ID:        "codex-alt",
		Provider:  codexauth.Provider,
		AuthMode:  "oauth",
		Email:     "alt@example.com",
		AccountID: "acct_alt",
		SecretRef: ref.String(),
	}); err != nil {
		t.Fatal(err)
	}
	var quotaCalls atomic.Int32
	quotaBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		quotaCalls.Add(1)
		used := 6
		if r.Header.Get("Authorization") == "Bearer alt-access-secret" {
			used = 3
		}
		json.NewEncoder(w).Encode(map[string]any{
			"planType": "team",
			"rateLimits": map[string]any{
				"primary": map[string]any{"usedPercent": used},
			},
			"debug": "backend-secret",
		})
	}))
	defer quotaBackend.Close()
	port := freeTCPPort(t)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", strconv.Itoa(port))
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	s := server.New(server.Options{
		Host: "127.0.0.1", Port: port, Token: token, Version: "it",
		Accounts: accounts, Secrets: secrets, RuntimeRoot: filepath.Join(home, "runtimes"),
		CodexQuotaBaseURL: quotaBackend.URL,
		Log:               slog.New(slog.NewTextHandler(io.Discard, nil)),
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

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"check", "--readiness"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `secret backend = "file", want "native"`) {
		t.Fatalf("err = %v out=%s", err, out.String())
	}
	if quotaCalls.Load() != 0 {
		t.Fatalf("readiness should fail backend preflight before quota calls, got %d", quotaCalls.Load())
	}

	out.Reset()
	cmd = newAccountsCmd()
	cmd.SetArgs([]string{"check", "--json", "--readiness", "--require-secret-backend", secret.BackendFile})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if quotaCalls.Load() != 2 {
		t.Fatalf("quota calls = %d", quotaCalls.Load())
	}
	var result protocol.AccountsCheckResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.CheckedAccounts != 2 || !result.QuotaRefreshed || result.SecretBackend != secret.BackendFile || result.AutoRoute == nil || result.AutoRoute.AccountID != "codex-alt" || !result.AutoRoute.Fresh {
		t.Fatalf("result = %+v", result)
	}
	for _, row := range result.Accounts {
		if !row.QuotaFresh || row.QuotaState != protocol.AccountQuotaStateFresh {
			t.Fatalf("row = %+v", row)
		}
	}
	for _, leaked := range []string{token, "access-secret", "alt-access-secret", "refresh-secret", "backend-secret", "secretRef", "secret_ref", "CODEX_HOME", filepath.Join(home, "runtimes")} {
		if strings.Contains(out.String(), leaked) {
			t.Fatalf("accounts check readiness leaked %q: %s", leaked, out.String())
		}
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
	if result.ID != "codex-test" || result.Provider != codexauth.Provider || result.Plan != "pro" || result.PrimaryUsedPercent != 37 || result.QuotaState != protocol.AccountQuotaStateFresh {
		t.Fatalf("result = %+v", result)
	}
	if q, err := accounts.LoadQuota("codex-test"); err != nil || q.PrimaryUsedPercent != 37 {
		t.Fatalf("cached quota = %+v err=%v", q, err)
	}
}

func TestCodexAccountsQuotaAutoUsesLowestCachedQuotaAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, secrets := seedCodexAccount(t)
	defer accounts.Close()
	ref, err := secrets.Put(context.Background(), "codex-low", secret.Bundle{
		Provider:     codexauth.Provider,
		AuthMode:     "chatgpt",
		AccessToken:  "low-access-secret",
		RefreshToken: "low-refresh-secret",
		AccountID:    "acct_low",
		RawAuthJSON:  []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"low-access-secret","refresh_token":"low-refresh-secret"}}`),
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
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", PrimaryUsedPercent: 80}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-low", PrimaryUsedPercent: 5}); err != nil {
		t.Fatal(err)
	}
	var sawAuth, sawAccount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawAccount = r.Header.Get("ChatGPT-Account-Id")
		json.NewEncoder(w).Encode(map[string]any{
			"planType": "team",
			"rateLimits": map[string]any{
				"primary": map[string]any{"usedPercent": 14},
			},
		})
	}))
	defer srv.Close()

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "quota", " auto ", "--base-url", srv.URL})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result codexQuotaSummary
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != "codex-low" || result.PrimaryUsedPercent != 14 || result.Plan != "team" {
		t.Fatalf("result = %+v", result)
	}
	if sawAuth != "Bearer low-access-secret" || sawAccount != "acct_low" {
		t.Fatalf("headers auth=%q account=%q", sawAuth, sawAccount)
	}
}

func TestCodexAccountsQuotaBackfillsMissingMetadataFromSecretAndPlan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, secrets := seedCodexAccount(t)
	defer accounts.Close()
	acc, err := accounts.LoadAccount("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	acc.Email = ""
	acc.AccountID = ""
	acc.Plan = ""
	if err := accounts.UpsertAccount(acc); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Put(context.Background(), "codex-test", secret.Bundle{
		Provider:     codexauth.Provider,
		AuthMode:     "chatgpt",
		AccessToken:  "access-secret",
		RefreshToken: "refresh-secret",
		Email:        "secret@example.com",
		AccountID:    "acct_secret",
		RawAuthJSON:  []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"access-secret","refresh_token":"refresh-secret","account_id":"acct_secret"}}`),
	}); err != nil {
		t.Fatal(err)
	}
	var sawAccount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAccount = r.Header.Get("ChatGPT-Account-Id")
		json.NewEncoder(w).Encode(map[string]any{
			"planType": "enterprise",
			"rateLimits": map[string]any{
				"primary": map[string]any{"usedPercent": 19},
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
	if sawAccount != "acct_secret" {
		t.Fatalf("ChatGPT-Account-Id = %q", sawAccount)
	}
	var result codexQuotaSummary
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Email != "secret@example.com" || result.AccountID != "acct_secret" || result.Plan != "enterprise" || result.PrimaryUsedPercent != 19 {
		t.Fatalf("result = %+v", result)
	}
	got, err := accounts.LoadAccount("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "secret@example.com" || got.AccountID != "acct_secret" || got.Plan != "enterprise" {
		t.Fatalf("stored account = %+v", got)
	}
}

func TestCodexAccountsQuotaAllRefreshesEveryAccountSafely(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, secrets := seedCodexAccount(t)
	defer accounts.Close()
	ref, err := secrets.Put(context.Background(), "codex-low", secret.Bundle{
		Provider:     codexauth.Provider,
		AuthMode:     "chatgpt",
		AccessToken:  "low-access-secret",
		RefreshToken: "low-refresh-secret",
		AccountID:    "acct_low",
		RawAuthJSON:  []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"low-access-secret","refresh_token":"low-refresh-secret"}}`),
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

	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		remoteAccount := r.Header.Get("ChatGPT-Account-Id")
		seen[auth] = remoteAccount
		used := 37
		plan := "pro"
		if auth == "Bearer low-access-secret" {
			used = 9
			plan = "team"
		}
		json.NewEncoder(w).Encode(map[string]any{
			"planType":   plan,
			"debugToken": "backend-secret",
			"rateLimits": map[string]any{
				"primary": map[string]any{"usedPercent": used},
			},
		})
	}))
	defer srv.Close()

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "quota", " all ", "--base-url", srv.URL})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, secret := range []string{"backend-secret", "debugToken", "access-secret", "refresh-secret", "low-access-secret", "low-refresh-secret", "rawJson", "RawJSON"} {
		if strings.Contains(text, secret) {
			t.Fatalf("quota all leaked %q: %s", secret, text)
		}
	}
	var rows []codexQuotaSummary
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].ID != "codex-low" || rows[1].ID != "codex-test" {
		t.Fatalf("rows not sorted by account id: %+v", rows)
	}
	byID := map[string]codexQuotaSummary{}
	for _, row := range rows {
		byID[row.ID] = row
		if row.Provider != codexauth.Provider || row.QuotaState != protocol.AccountQuotaStateFresh || row.CheckedAt == 0 {
			t.Fatalf("row = %+v", row)
		}
	}
	if byID["codex-test"].PrimaryUsedPercent != 37 || byID["codex-test"].Plan != "pro" || byID["codex-test"].AccountID != "acct_test" {
		t.Fatalf("codex-test row = %+v", byID["codex-test"])
	}
	if byID["codex-low"].PrimaryUsedPercent != 9 || byID["codex-low"].Plan != "team" || byID["codex-low"].AccountID != "acct_low" {
		t.Fatalf("codex-low row = %+v", byID["codex-low"])
	}
	if seen["Bearer access-secret"] != "acct_test" || seen["Bearer low-access-secret"] != "acct_low" {
		t.Fatalf("headers = %+v", seen)
	}
	for id, want := range map[string]float64{"codex-test": 37, "codex-low": 9} {
		q, err := accounts.LoadQuota(id)
		if err != nil || q.PrimaryUsedPercent != want {
			t.Fatalf("%s cached quota = %+v err=%v", id, q, err)
		}
	}
}

func TestCodexAccountsQuotaAllRejectsRawOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "quota", "all", "--raw"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--raw is only supported for a single account") {
		t.Fatalf("err = %v", err)
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

func TestCodexAccountsQuotaRejectsSecretBackendMismatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatal("quota backend should not be called when secret backend mismatches")
	}))
	defer srv.Close()

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "quota", "--base-url", srv.URL})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `secret backend = "native", active backend = "file"`) {
		t.Fatalf("err = %v", err)
	}
	if called {
		t.Fatal("quota backend was called")
	}
	for _, leaked := range []string{"access-secret", "refresh-secret", "native:codex-test"} {
		if strings.Contains(err.Error(), leaked) || strings.Contains(out.String(), leaked) {
			t.Fatalf("quota leaked %q: err=%v out=%s", leaked, err, out.String())
		}
	}
}

func TestAccountsCheckRefreshQuotaFailureDoesNotLeakSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-refresh-fail"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	accounts, secrets := seedCodexAccount(t)
	var quotaCalls atomic.Int32
	quotaBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		quotaCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer access-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "acct_test" {
			t.Fatalf("ChatGPT-Account-Id = %q", got)
		}
		http.Error(w, "backend-secret access-secret secretRef=file:codex-test", http.StatusTooManyRequests)
	}))
	defer quotaBackend.Close()
	port := freeTCPPort(t)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", strconv.Itoa(port))
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	s := server.New(server.Options{
		Host: "127.0.0.1", Port: port, Token: token, Version: "it",
		Accounts: accounts, Secrets: secrets, RuntimeRoot: filepath.Join(home, "runtimes"),
		CodexQuotaBaseURL: quotaBackend.URL,
		Log:               slog.New(slog.NewTextHandler(io.Discard, nil)),
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
		accounts.Close()
	})

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"check", "--refresh-quota", "--require-fresh-quota", "--require-all-fresh-quota"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "codex-test") || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("err = %v", err)
	}
	if quotaCalls.Load() != 1 {
		t.Fatalf("quota calls = %d", quotaCalls.Load())
	}
	for _, leaked := range []string{token, "access-secret", "refresh-secret", "backend-secret", "secretRef", "secret_ref", "CODEX_HOME", filepath.Join(home, "runtimes")} {
		if strings.Contains(err.Error(), leaked) || strings.Contains(out.String(), leaked) {
			t.Fatalf("accounts check refresh failure leaked %q: err=%v out=%s", leaked, err, out.String())
		}
	}
}

func TestAccountsCheckRejectsUnknownRequiredSecretBackend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"check", "--require-secret-backend", "mystery"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `unknown secret backend "mystery"`) {
		t.Fatalf("err = %v", err)
	}
	for _, leaked := range []string{"access-secret", "refresh-secret", "secretRef", "CODEX_HOME"} {
		if strings.Contains(out.String(), leaked) {
			t.Fatalf("accounts check leaked %q: %s", leaked, out.String())
		}
	}
}

func TestAccountsCheckRequireAllFreshQuotaFailsWithoutAccounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-no-accounts"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	accounts, err := account.OpenStore(filepath.Join(home, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	secrets := secret.NewFileStore(filepath.Join(home, "secrets", "codex"))
	port := freeTCPPort(t)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", strconv.Itoa(port))
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
		accounts.Close()
	})

	for _, args := range [][]string{
		{"check", "--require-fresh-quota"},
		{"check", "--require-all-fresh-quota"},
	} {
		var out bytes.Buffer
		cmd := newAccountsCmd()
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err = cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "no Codex accounts checked") {
			t.Fatalf("%v err = %v", args, err)
		}
		for _, leaked := range []string{token, "CODEX_HOME", filepath.Join(home, "runtimes"), filepath.Join(home, "accounts.db")} {
			if strings.Contains(err.Error(), leaked) || strings.Contains(out.String(), leaked) {
				t.Fatalf("accounts check empty gate leaked %q: err=%v out=%s", leaked, err, out.String())
			}
		}
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
	for _, want := range []string{"PROVIDER", "QUOTA_STATE", "codex-test", "gemini-test", "gemini@example.com", "0.0%", protocol.AccountQuotaStateFresh, protocol.AccountQuotaStateMissing} {
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
		ID:        "codex-zlow",
		Provider:  codexauth.Provider,
		AuthMode:  "oauth",
		Email:     "zlow@example.com",
		SecretRef: "file:codex-zlow",
	}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.UpsertAccount(account.Account{
		ID:        "gemini-test",
		Provider:  "gemini",
		AuthMode:  "oauth",
		Email:     "gemini@example.com",
		SecretRef: "file:gemini-secret",
	}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 0}); err != nil {
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
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Provider != codexauth.Provider || rows[0].ID != "codex-test" || !rows[0].Current {
		t.Fatalf("first row = %+v", rows[0])
	}
	if rows[0].QuotaState != protocol.AccountQuotaStateFresh || rows[0].QuotaCheckedAt == 0 || rows[0].PrimaryUsed != "0.0%" {
		t.Fatalf("first row quota = %+v", rows[0])
	}
	if rows[1].Provider != codexauth.Provider || rows[1].ID != "codex-zlow" || rows[1].Current {
		t.Fatalf("second row = %+v", rows[1])
	}
	if rows[1].QuotaState != protocol.AccountQuotaStateMissing || rows[1].QuotaCheckedAt != 0 {
		t.Fatalf("second row quota = %+v", rows[1])
	}
	if rows[2].Provider != "gemini" || rows[2].ID != "gemini-test" {
		t.Fatalf("third row = %+v", rows[2])
	}
	if rows[2].QuotaState != protocol.AccountQuotaStateMissing || rows[2].QuotaCheckedAt != 0 {
		t.Fatalf("third row quota = %+v", rows[2])
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
	if acc.ID != "codex-test" || !acc.ProjectionOK || !acc.RuntimeEnvOK || !acc.AuthJSONPrivate || !acc.ProjectionMarkerOK || !acc.SecretBackendOK || !acc.SecretReadable || acc.PrimaryUsed != "0.0%" || acc.PrimaryUsedPercent == nil || *acc.PrimaryUsedPercent != 0 || acc.QuotaState != protocol.AccountQuotaStateFresh || !acc.QuotaFresh || acc.QuotaCheckedAt == 0 {
		t.Fatalf("account = %+v", acc)
	}
	if result.AutoRoute == nil || result.AutoRoute.AccountID != "codex-test" {
		t.Fatalf("auto route = %+v", result.AutoRoute)
	}
}

func TestCodexAccountsSmokeQuotaBackfillsMissingMetadata(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, secrets := seedCodexAccount(t)
	defer accounts.Close()
	acc, err := accounts.LoadAccount("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	acc.Email = ""
	acc.AccountID = ""
	acc.Plan = ""
	if err := accounts.UpsertAccount(acc); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Put(context.Background(), "codex-test", secret.Bundle{
		Provider:     codexauth.Provider,
		AuthMode:     "chatgpt",
		AccessToken:  "access-secret",
		RefreshToken: "refresh-secret",
		Email:        "secret@example.com",
		AccountID:    "acct_secret",
		RawAuthJSON:  []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"access-secret","refresh_token":"refresh-secret","account_id":"acct_secret"},"last_refresh":"2026-06-01T00:00:00Z"}`),
	}); err != nil {
		t.Fatal(err)
	}
	var sawAccount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAccount = r.Header.Get("ChatGPT-Account-Id")
		json.NewEncoder(w).Encode(map[string]any{
			"planType": "enterprise",
			"rateLimits": map[string]any{
				"primary": map[string]any{"usedPercent": 21},
			},
		})
	}))
	defer srv.Close()

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke", "--json", "--quota", "--base-url", srv.URL})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if sawAccount != "acct_secret" {
		t.Fatalf("ChatGPT-Account-Id = %q", sawAccount)
	}
	var result codexSmokeResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].Email != "secret@example.com" || result.Accounts[0].PrimaryUsedPercent == nil || *result.Accounts[0].PrimaryUsedPercent != 21 {
		t.Fatalf("result = %+v", result)
	}
	got, err := accounts.LoadAccount("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "secret@example.com" || got.AccountID != "acct_secret" || got.Plan != "enterprise" {
		t.Fatalf("stored account = %+v", got)
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
		if !acc.ProjectionOK || !acc.RuntimeEnvOK || !acc.AuthJSONPrivate || !acc.ProjectionMarkerOK || !acc.SecretBackendOK || !acc.SecretReadable || acc.QuotaState != protocol.AccountQuotaStateFresh || !acc.QuotaFresh {
			t.Fatalf("projection evidence missing: %+v", acc)
		}
	}
	if result.AutoRoute.AccountID != "codex-low" || result.AutoRoute.QuotaState != protocol.AccountQuotaStateFresh || !result.AutoRoute.Fresh || result.AutoRoute.Primary == nil || *result.AutoRoute.Primary != 4 {
		t.Fatalf("auto route = %+v", result.AutoRoute)
	}
	if !strings.Contains(result.AutoRoute.Reason, "fresh primary quota") {
		t.Fatalf("auto route reason = %q", result.AutoRoute.Reason)
	}
}

func TestCodexAccountsSmokeJSONSortsAccountsByID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, secrets := seedCodexAccount(t)
	defer accounts.Close()
	ref, err := secrets.Put(context.Background(), "codex-zlow", secret.Bundle{
		Provider:     codexauth.Provider,
		AuthMode:     "chatgpt",
		AccessToken:  "zlow-access-secret",
		RefreshToken: "zlow-refresh-secret",
		RawAuthJSON:  []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"zlow-access-secret","refresh_token":"zlow-refresh-secret"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.UpsertAccount(account.Account{
		ID:        "codex-zlow",
		Provider:  codexauth.Provider,
		AuthMode:  "chatgpt",
		Email:     "zlow@example.com",
		AccountID: "acct_zlow",
		SecretRef: ref.String(),
	}); err != nil {
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
	var result codexSmokeResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Accounts) != 2 || result.Accounts[0].ID != "codex-test" || result.Accounts[1].ID != "codex-zlow" {
		t.Fatalf("accounts not sorted by account id: %+v", result.Accounts)
	}
}

func TestCodexAccountsSmokeRequireFreshQuotaFailsWhenMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke", "--require-fresh-quota"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "fresh cached quota") {
		t.Fatalf("err = %v", err)
	}
}

func TestCodexAccountsSmokeJSONMarksAutoRouteMissingQuota(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke", "--json"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result codexSmokeResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AutoRoute == nil || result.AutoRoute.AccountID != "codex-test" || result.AutoRoute.QuotaState != protocol.AccountQuotaStateMissing || result.AutoRoute.Fresh || result.AutoRoute.Primary != nil {
		t.Fatalf("auto route = %+v", result.AutoRoute)
	}
	if len(result.RouteCandidates) != 1 || result.RouteCandidates[0].AccountID != "codex-test" || result.RouteCandidates[0].QuotaState != protocol.AccountQuotaStateMissing || result.RouteCandidates[0].Fresh {
		t.Fatalf("route candidates = %+v", result.RouteCandidates)
	}
	if result.Accounts[0].QuotaState != protocol.AccountQuotaStateMissing || result.Accounts[0].QuotaFresh || result.Accounts[0].PrimaryUsed != "cached-missing" || result.Accounts[0].PrimaryUsedPercent != nil {
		t.Fatalf("account quota = %+v", result.Accounts[0])
	}
}

func TestCodexAccountsSmokeJSONMarksAutoRouteStaleQuota(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	staleAt := time.Now().Add(-account.QuotaRouteCacheTTL - time.Minute).Unix()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 2, CheckedAt: staleAt}); err != nil {
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
	var result codexSmokeResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AutoRoute == nil || result.AutoRoute.AccountID != "codex-test" || result.AutoRoute.QuotaState != protocol.AccountQuotaStateStale || result.AutoRoute.Fresh || result.AutoRoute.Primary == nil || *result.AutoRoute.Primary != 2 || result.AutoRoute.CheckedAt != staleAt {
		t.Fatalf("auto route = %+v", result.AutoRoute)
	}
	if len(result.RouteCandidates) != 1 || result.RouteCandidates[0].AccountID != "codex-test" || result.RouteCandidates[0].QuotaState != protocol.AccountQuotaStateStale || result.RouteCandidates[0].Fresh {
		t.Fatalf("route candidates = %+v", result.RouteCandidates)
	}
	if result.Accounts[0].QuotaState != protocol.AccountQuotaStateStale || result.Accounts[0].QuotaFresh || result.Accounts[0].QuotaCheckedAt != staleAt || result.Accounts[0].PrimaryUsedPercent == nil || *result.Accounts[0].PrimaryUsedPercent != 2 {
		t.Fatalf("account quota = %+v", result.Accounts[0])
	}
}

func TestCodexAccountsSmokeRequireFreshQuotaPassesWithFreshCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 9}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke", "--json", "--require-fresh-quota"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result codexSmokeResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AutoRoute == nil || result.AutoRoute.QuotaState != protocol.AccountQuotaStateFresh || !result.AutoRoute.Fresh || result.AutoRoute.Primary == nil || *result.AutoRoute.Primary != 9 {
		t.Fatalf("auto route = %+v", result.AutoRoute)
	}
	if len(result.RouteCandidates) != 1 || result.RouteCandidates[0].AccountID != "codex-test" || !result.RouteCandidates[0].Fresh || result.RouteCandidates[0].PrimaryUsedPercent == nil || *result.RouteCandidates[0].PrimaryUsedPercent != 9 {
		t.Fatalf("route candidates = %+v", result.RouteCandidates)
	}
}

func TestCodexAccountsSmokeTextIncludesAutoRouteEvidence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 9}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke", "--require-fresh-quota"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"auto route: codex-test quota fresh fresh true primary 9.0% score ",
		"route candidates:",
		"codex-test quota fresh fresh true primary 9.0% score ",
		"checked ",
		"secret backend: file",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("smoke text missing %q: %s", want, text)
		}
	}
	for _, leaked := range []string{"access-secret", "refresh-secret", "secretRef"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("smoke text leaked %q: %s", leaked, text)
		}
	}
}

func TestCodexAccountsSmokeRequireAllFreshQuota(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, secrets := seedCodexAccount(t)
	defer accounts.Close()
	ref, err := secrets.Put(context.Background(), "codex-low", secret.Bundle{
		Provider:     codexauth.Provider,
		AuthMode:     "chatgpt",
		AccessToken:  "low-access-secret",
		RefreshToken: "low-refresh-secret",
		RawAuthJSON:  []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"low-access-secret","refresh_token":"low-refresh-secret"}}`),
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
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 9}); err != nil {
		t.Fatal(err)
	}
	staleAt := time.Now().Add(-account.QuotaRouteCacheTTL - time.Minute).Unix()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-low", Plan: "pro", PrimaryUsedPercent: 3, CheckedAt: staleAt}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke", "--require-all-fresh-quota"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "quota is not fresh for codex-low=stale") {
		t.Fatalf("err = %v", err)
	}

	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-low", Plan: "pro", PrimaryUsedPercent: 3}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	cmd = newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke", "--json", "--require-all-fresh-quota"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result codexSmokeResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, acc := range result.Accounts {
		if acc.QuotaState != protocol.AccountQuotaStateFresh || !acc.QuotaFresh {
			t.Fatalf("account quota = %+v", acc)
		}
	}
}

func TestCodexAccountsSmokeJSONFailureKeepsPartialEvidence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 4}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke", "--json", "--require-multiple", "--require-secret-backend", "file"})
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "expected multiple Codex accounts, found 1") {
		t.Fatalf("err = %v", err)
	}
	var got codexSmokeResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json err = %v output=%s stderr=%s", err, out.String(), errOut.String())
	}
	if got.OK || got.CheckedAccounts != 1 || got.SecretBackend != "file" || !containsString(got.Issues, "expected multiple Codex accounts, found 1") {
		t.Fatalf("result = %+v", got)
	}
	if len(got.RouteCandidates) != 0 || len(got.Accounts) != 0 {
		t.Fatalf("preflight failure should not project accounts before require-multiple passes: %+v", got)
	}
	for _, secret := range []string{"access-secret", "refresh-secret", "secretRef", "secret_ref", "rawAuthJson"} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("smoke failure JSON leaked %q: %s", secret, out.String())
		}
	}
}

func TestCodexAccountsSmokeRequireSecretBackend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke", "--require-secret-backend", secret.BackendFile})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "secret backend: file") {
		t.Fatalf("output = %s", out.String())
	}
}

func TestCodexAccountsSmokeRequireSecretBackendMismatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke", "--require-secret-backend", secret.BackendNative})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `want "native"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestCodexAccountsSmokeRejectsUnknownRequiredSecretBackend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke", "--require-secret-backend", "mystery"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `unknown secret backend "mystery"`) {
		t.Fatalf("err = %v", err)
	}
	for _, leaked := range []string{"access-secret", "refresh-secret", "secretRef", "CODEX_HOME"} {
		if strings.Contains(out.String(), leaked) {
			t.Fatalf("smoke leaked %q: %s", leaked, out.String())
		}
	}
}

func TestCodexAccountsSmokeFailsWhenAccountSecretBackendDiffers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `secret backend = "native", active backend = "file"`) {
		t.Fatalf("err = %v", err)
	}
	for _, leaked := range []string{"access-secret", "refresh-secret", "native:codex-test"} {
		if strings.Contains(err.Error(), leaked) || strings.Contains(out.String(), leaked) {
			t.Fatalf("smoke leaked %q: err=%v out=%s", leaked, err, out.String())
		}
	}
}

func TestAccountsSecretBackendFlagSelectsBackend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"--secret-backend", secret.BackendFile, "codex", "smoke", "--require-secret-backend", secret.BackendFile})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "secret backend: file") {
		t.Fatalf("output = %s", out.String())
	}
}

func TestAccountsSecretBackendFlagRejectsUnknownBackend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"--secret-backend", "mystery", "list"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown secret backend") {
		t.Fatalf("err = %v", err)
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

func TestCodexAccountsRemoveDeletesMetadataAndSecret(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, secrets := seedCodexAccount(t)
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", PrimaryUsedPercent: 11}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.BindSessionAccount("s_1", "codex-test"); err != nil {
		t.Fatal(err)
	}
	accounts.Close()

	var projectOut bytes.Buffer
	projectCmd := newAccountsCmd()
	projectCmd.SetArgs([]string{"codex", "project", "codex-test"})
	projectCmd.SetOut(&projectOut)
	projectCmd.SetErr(&projectOut)
	if err := projectCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	codexHome := strings.TrimSpace(projectOut.String())
	if _, err := os.Stat(filepath.Join(codexHome, "auth.json")); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "remove", "codex-test"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "removed codex-test") {
		t.Fatalf("output = %s", text)
	}
	for _, leaked := range []string{"access-secret", "refresh-secret", "secretRef", "file:codex-test"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("remove output leaked %q: %s", leaked, text)
		}
	}
	if _, err := secrets.Get(context.Background(), secret.Ref{Backend: secret.BackendFile, ID: "codex-test"}); err == nil {
		t.Fatal("secret still readable after remove")
	}
	if _, err := os.Stat(codexHome); !os.IsNotExist(err) {
		t.Fatalf("projection still exists err=%v", err)
	}

	home, err := daemon.Home()
	if err != nil {
		t.Fatal(err)
	}
	accounts, err = account.OpenStore(filepath.Join(home, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer accounts.Close()
	if _, err := accounts.LoadAccount("codex-test"); !errors.Is(err, account.ErrUnknownAccount) {
		t.Fatalf("deleted account err = %v", err)
	}
	if _, err := accounts.LoadQuota("codex-test"); !errors.Is(err, account.ErrUnknownAccount) {
		t.Fatalf("deleted quota err = %v", err)
	}
	sessionAccount, err := accounts.SessionAccount("s_1")
	if err != nil {
		t.Fatal(err)
	}
	if sessionAccount != "" {
		t.Fatalf("session account = %q", sessionAccount)
	}
	current, err := accounts.CurrentAccount(codexauth.Provider)
	if err != nil {
		t.Fatal(err)
	}
	if current != "" {
		t.Fatalf("current = %q", current)
	}
}

func TestCodexConcreteAccountCommandsRejectAutoAndAll(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	accounts.Close()

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"current auto", []string{"codex", "current", protocol.AccountAuto}, "account-aware routing"},
		{"current all", []string{"codex", "current", protocol.AccountAll}, "quota batch refresh"},
		{"project auto", []string{"codex", "project", protocol.AccountAuto}, "account-aware routing"},
		{"project all", []string{"codex", "project", protocol.AccountAll}, "quota batch refresh"},
		{"remove auto", []string{"codex", "remove", protocol.AccountAuto}, "account-aware routing"},
		{"remove all", []string{"codex", "remove", protocol.AccountAll}, "quota batch refresh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			cmd := newAccountsCmd()
			cmd.SetArgs(tc.args)
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
			for _, leaked := range []string{"access-secret", "refresh-secret", "file:codex-test"} {
				if strings.Contains(err.Error(), leaked) || strings.Contains(out.String(), leaked) {
					t.Fatalf("leaked %q: err=%v out=%s", leaked, err, out.String())
				}
			}
		})
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
	acc, err := resolveUsageAccount(accounts, " auto ")
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

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitForHealthz(t *testing.T, port int) {
	t.Helper()
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/healthz"
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not become healthy at %s", url)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
