package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	if !strings.Contains(text, "0.0%") || !strings.Contains(text, "SECRET_BACKEND") || !strings.Contains(text, secret.BackendFile) || !strings.Contains(text, "QUOTA_STATE") || !strings.Contains(text, protocol.AccountQuotaStateFresh) || !strings.Contains(text, "FRESH") || !strings.Contains(text, "ROUTE_SCORE") || !strings.Contains(text, "true") || !strings.Contains(text, "-0.01") {
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
	if !row.Current || row.Provider != codexauth.Provider || row.ID != "codex-test" || row.SecretBackend != secret.BackendFile || row.Plan != "pro" || row.PrimaryUsed != "0.0%" || row.QuotaState != protocol.AccountQuotaStateFresh || !row.QuotaFresh || row.QuotaCheckedAt != checkedAt || row.RouteScore == nil || *row.RouteScore != -0.01 || row.RouteReason != "auto account codex-test primary 0%; current account tie-break" {
		t.Fatalf("row = %+v", row)
	}
}

func TestMigrateCodexAccountSecretsMovesToTargetBackend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, source := seedCodexAccount(t)
	defer accounts.Close()
	target := newMemorySecretStore(secret.BackendNative)

	result, err := migrateCodexAccountSecrets(context.Background(), accounts, source, target, codexSecretMigrationOptions{
		AccountID:     protocol.AccountAll,
		SourceBackend: secret.BackendFile,
		TargetBackend: secret.BackendNative,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Migrated) != 1 || result.Migrated[0].Status != "migrated" || result.Migrated[0].From != secret.BackendFile || result.Migrated[0].To != "native:codex-test" {
		t.Fatalf("result = %+v", result)
	}
	if !containsString(result.NextSteps, "restart capd with CAPD_SECRET_BACKEND=native and run: CAPD_SECRET_BACKEND=native capd accounts check --json --readiness --require-secret-backend native --timeout 2m") {
		t.Fatalf("nextSteps = %+v", result.NextSteps)
	}
	acc, err := accounts.LoadAccount("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	if acc.SecretRef != "native:codex-test" {
		t.Fatalf("secret ref = %q", acc.SecretRef)
	}
	bundle, err := target.Get(context.Background(), secret.Ref{Backend: secret.BackendNative, ID: "codex-test"})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.AccessToken != "access-secret" || bundle.RefreshToken != "refresh-secret" {
		t.Fatalf("bundle metadata lost: %+v", bundle)
	}
	if _, err := source.Get(context.Background(), secret.Ref{Backend: secret.BackendFile, ID: "codex-test"}); err != nil {
		t.Fatalf("source should remain by default: %v", err)
	}
	data, _ := json.Marshal(result)
	for _, leaked := range []string{"access-secret", "refresh-secret", "rawAuthJson", "secretRef"} {
		if strings.Contains(string(data), leaked) {
			t.Fatalf("migration result leaked %q: %s", leaked, data)
		}
	}
}

func TestMigrateCodexAccountSecretsDryRunAndDeleteSource(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, source := seedCodexAccount(t)
	defer accounts.Close()
	target := newMemorySecretStore(secret.BackendNative)

	dryRun, err := migrateCodexAccountSecrets(context.Background(), accounts, source, target, codexSecretMigrationOptions{
		AccountID:     "codex-test",
		DryRun:        true,
		SourceBackend: secret.BackendFile,
		TargetBackend: secret.BackendNative,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !dryRun.OK || !dryRun.DryRun || len(dryRun.Migrated) != 1 || dryRun.Migrated[0].Status != "dry-run" {
		t.Fatalf("dry run = %+v", dryRun)
	}
	acc, err := accounts.LoadAccount("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	if acc.SecretRef != "file:codex-test" {
		t.Fatalf("dry run changed secret ref: %q", acc.SecretRef)
	}
	if _, err := target.Get(context.Background(), secret.Ref{Backend: secret.BackendNative, ID: "codex-test"}); err == nil {
		t.Fatal("dry run wrote target secret")
	}

	result, err := migrateCodexAccountSecrets(context.Background(), accounts, source, target, codexSecretMigrationOptions{
		AccountID:     "codex-test",
		DeleteSource:  true,
		SourceBackend: secret.BackendFile,
		TargetBackend: secret.BackendNative,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || !result.DeleteSource || len(result.Migrated) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := source.Get(context.Background(), secret.Ref{Backend: secret.BackendFile, ID: "codex-test"}); err == nil {
		t.Fatal("source secret still exists after --delete-source")
	}
}

func TestMigrateCodexAccountSecretsVerifiesTargetReadableBeforeMetadataUpdate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, source := seedCodexAccount(t)
	defer accounts.Close()
	target := &unreadableSecretStore{memorySecretStore: newMemorySecretStore(secret.BackendNative)}

	result, err := migrateCodexAccountSecrets(context.Background(), accounts, source, target, codexSecretMigrationOptions{
		AccountID:     "codex-test",
		DeleteSource:  true,
		SourceBackend: secret.BackendFile,
		TargetBackend: secret.BackendNative,
	})
	if err == nil || err.Error() != "target-unreadable" {
		t.Fatalf("err = %v result=%+v", err, result)
	}
	if result.OK || len(result.Migrated) != 1 || result.Migrated[0].Status != "failed" || result.Migrated[0].Reason != "target-unreadable" {
		t.Fatalf("result = %+v", result)
	}
	acc, err := accounts.LoadAccount("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	if acc.SecretRef != "file:codex-test" {
		t.Fatalf("metadata updated despite unreadable target: %q", acc.SecretRef)
	}
	if _, err := source.Get(context.Background(), secret.Ref{Backend: secret.BackendFile, ID: "codex-test"}); err != nil {
		t.Fatalf("source should remain after unreadable target: %v", err)
	}
	if _, ok := target.bundles["codex-test"]; ok {
		t.Fatal("unreadable target secret was not cleaned up")
	}
	data, _ := json.Marshal(result)
	for _, leaked := range []string{"access-secret", "refresh-secret", "rawAuthJson", "secretRef"} {
		if strings.Contains(string(data), leaked) {
			t.Fatalf("migration failure leaked %q: %s", leaked, data)
		}
	}
}

func TestMigrateCodexAccountSecretsSkipsSourceMismatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	source := newMemorySecretStore(secret.BackendNative)
	target := secret.NewFileStore(filepath.Join(t.TempDir(), "target-secrets"))

	result, err := migrateCodexAccountSecrets(context.Background(), accounts, source, target, codexSecretMigrationOptions{
		AccountID:     protocol.AccountAll,
		SourceBackend: secret.BackendNative,
		TargetBackend: secret.BackendFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Migrated) != 0 || len(result.Skipped) != 1 || result.Skipped[0].Reason != "source-backend-mismatch" {
		t.Fatalf("result = %+v", result)
	}
}

func TestAccountSecretBackendEvidenceIsSafe(t *testing.T) {
	for _, tc := range []struct {
		ref  string
		want string
	}{
		{ref: "file:codex-test", want: secret.BackendFile},
		{ref: "native:codex-test", want: secret.BackendNative},
		{ref: "legacy-id", want: secret.BackendFile},
		{ref: "native:", want: "malformed"},
	} {
		if got := accountSecretBackend(tc.ref); got != tc.want {
			t.Fatalf("accountSecretBackend(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}
}

func TestCodexAccountsMigrateSecretsHelp(t *testing.T) {
	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "migrate-secrets", "--help"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"migrate-secrets", "--from", "--to", "--delete-source", "--dry-run", "--timeout", "--json", "native"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help missing %q: %s", want, text)
		}
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
		"--timeout",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("help missing %q: %s", needle, text)
		}
	}
}

func TestCodexAccountsLocalCommandsExposeTimeouts(t *testing.T) {
	for _, args := range [][]string{
		{"codex", "quota", "--help"},
		{"codex", "smoke", "--help"},
	} {
		var out bytes.Buffer
		cmd := newAccountsCmd()
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		text := out.String()
		if !strings.Contains(text, "--timeout") || !strings.Contains(text, "2m") {
			t.Fatalf("%v help missing timeout: %s", args, text)
		}
	}
}

func TestPrintAccountsCheckJSONErrorHandlesGenericError(t *testing.T) {
	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetOut(&out)
	printAccountsCheckJSONError(cmd, fmt.Errorf("context deadline exceeded"))
	var got accountsCheckJSONError
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK || got.Error.Code != protocol.CodeInternalError || !strings.Contains(got.Error.Message, "context deadline exceeded") {
		t.Fatalf("error json = %+v", got)
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
	if row.ID != "codex-test" || !row.Current || !row.SecretBackendOK || row.SecretState != protocol.AccountSecretStateReadable || !row.CredentialReadable || !row.RuntimeReady || !row.AuthJSONPrivate || !row.ProjectionMarkerOK || row.QuotaState != protocol.AccountQuotaStateFresh || !row.QuotaFresh {
		t.Fatalf("row = %+v", row)
	}
	if result.AutoRoute == nil || result.AutoRoute.AccountID != "codex-test" || result.AutoRoute.QuotaState != protocol.AccountQuotaStateFresh {
		t.Fatalf("auto route = %+v", result.AutoRoute)
	}
	if len(result.RouteCandidates) != 1 || result.RouteCandidates[0].AccountID != "codex-test" || !result.RouteCandidates[0].Fresh {
		t.Fatalf("route candidates = %+v", result.RouteCandidates)
	}
	if !result.Summary.Ready || result.Summary.CheckedAccounts != 1 || result.Summary.RequiredAccounts != 2 || result.Summary.MissingAccounts != 1 || result.Summary.FreshQuotaAccounts != 1 || !result.Summary.AutoRouteFresh || !result.Summary.RouteDecisionOK || result.Summary.RouteCandidates != 1 || !result.Summary.SecretBackendOK {
		t.Fatalf("summary = %+v", result.Summary)
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
	for _, want := range []string{"summary: ready=true accounts=1/2 missing=1 quota fresh=1 stale=0 missing=0 autoFresh=true routeDecision=true secretOK=true", "auto route: codex-test quota fresh fresh true secret file primary 12.0% score ", "SECRET_STATE", "FRESH", "PRIMARY", "CHECKED_AT", protocol.AccountSecretStateReadable, protocol.AccountQuotaStateFresh, "true", "12.0%"} {
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
	if !refreshed.Summary.Ready || !refreshed.Summary.QuotaRefreshed || refreshed.Summary.FreshQuotaAccounts != 1 || !refreshed.Summary.AutoRouteFresh || !refreshed.Summary.RouteDecisionOK {
		t.Fatalf("refreshed summary = %+v", refreshed.Summary)
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
	for _, want := range []string{"auto route: codex-test quota fresh fresh true secret file primary 6.0% score ", "SECRET_STATE", "FRESH", "PRIMARY", "CHECKED_AT", protocol.AccountSecretStateReadable, protocol.AccountQuotaStateFresh, "true", "6.0%"} {
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
	if !containsString(failure.NextSteps, "restart capd with: capd start --secret-backend native") {
		t.Fatalf("json failure nextSteps = %+v", failure.NextSteps)
	}
	var partial protocol.AccountsCheckResult
	if unmarshalErr := json.Unmarshal(failure.Data, &partial); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if partial.CheckedAccounts != 1 || partial.SecretBackend != secret.BackendFile || len(partial.Accounts) != 1 || partial.AutoRoute == nil || len(partial.RouteCandidates) != 1 {
		t.Fatalf("partial = %+v", partial)
	}
	if len(partial.RepairPlan) == 0 || !containsRepairPlanStep(partial.RepairPlan, "restart-daemon-secret-backend") {
		t.Fatalf("partial repair plan = %+v", partial.RepairPlan)
	}
	if partial.Accounts[0].ID != "codex-test" || partial.Accounts[0].SecretBackendOK || partial.Accounts[0].CredentialReadable || partial.Accounts[0].RuntimeReady {
		t.Fatalf("partial cached account should not read secret or project runtime: %+v", partial.Accounts[0])
	}
	if partial.AutoRoute.AccountID != "codex-test" || partial.RouteCandidates[0].Reason != "auto account codex-test primary 6%; current account tie-break" {
		t.Fatalf("partial route evidence = auto:%+v candidates:%+v", partial.AutoRoute, partial.RouteCandidates)
	}
	for _, leaked := range []string{token, "access-secret", "refresh-secret", "secretRef", "secret_ref", "CODEX_HOME", filepath.Join(home, "runtimes")} {
		if strings.Contains(out.String(), leaked) {
			t.Fatalf("accounts check json gate leaked %q: %s", leaked, out.String())
		}
	}
}

func TestAccountsCheckJSONErrorSuggestsSecondAccountSafely(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-check-second"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	accounts, secrets := seedCodexAccount(t)
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 12}); err != nil {
		t.Fatal(err)
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
		accounts.Close()
	})

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"check", "--json", "--require-multiple"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "expected multiple Codex accounts") {
		t.Fatalf("err = %v", err)
	}
	var failure accountsCheckJSONError
	if err := json.Unmarshal(out.Bytes(), &failure); err != nil {
		t.Fatalf("json error output = %q: %v", out.String(), err)
	}
	for _, want := range []string{
		daemonSecondImportNextStep(),
		"or import locally with: import a second Codex account with: capd accounts codex import --auth /path/to/auth.json, or batch import with: " + codexAuthPathsEnvExample("capd accounts codex import"),
	} {
		if !containsString(failure.NextSteps, want) {
			t.Fatalf("json failure nextSteps missing %q: %+v", want, failure.NextSteps)
		}
	}
	for _, leaked := range []string{token, "access-secret", "refresh-secret", "secretRef", "secret_ref", "CODEX_HOME", filepath.Join(home, "runtimes")} {
		if strings.Contains(out.String(), leaked) {
			t.Fatalf("accounts check json next steps leaked %q: %s", leaked, out.String())
		}
	}
}

func TestAccountsCheckErrorNextStepsExplainSecretAccessDenied(t *testing.T) {
	fromMessage := accountsCheckErrorNextSteps("refresh quota: codex-test: load account secret: macOS keychain status -128", protocol.AccountsCheckResult{})
	for _, want := range []string{"approve macOS Keychain access", "capd start --secret-backend file", "capd accounts --secret-backend file codex import"} {
		if len(fromMessage) == 0 || !strings.Contains(fromMessage[0], want) {
			t.Fatalf("message nextSteps missing %q: %+v", want, fromMessage)
		}
	}

	fromEvidence := accountsCheckErrorNextSteps("load account credentials: unreadable", protocol.AccountsCheckResult{
		Accounts: []protocol.AccountCheckEvidence{{ID: "codex-test", SecretState: protocol.AccountSecretStateAccessDenied}},
	})
	if len(fromEvidence) != 1 || !strings.Contains(fromEvidence[0], "approve macOS Keychain access") {
		t.Fatalf("evidence nextSteps = %+v", fromEvidence)
	}

	unreadable := accountsCheckErrorNextSteps("load account credentials: unreadable", protocol.AccountsCheckResult{
		SecretBackend: secret.BackendNative,
		Accounts:      []protocol.AccountCheckEvidence{{ID: "codex-test", SecretState: protocol.AccountSecretStateUnreadable}},
	})
	if !containsString(unreadable, "verify SecretStore directly with: capd secretstore check --json --roundtrip --secret-backend native --require-backend native --timeout 2m, then re-import affected Codex accounts through CAP with: capd accounts import --auth /path/to/auth.json") {
		t.Fatalf("unreadable nextSteps = %+v", unreadable)
	}
	if got := accountsCheckSecretStoreCommand(""); got != "capd secretstore check --json --roundtrip --timeout 2m" {
		t.Fatalf("generic secretstore command = %q", got)
	}
}

func TestAccountsCheckErrorNextStepsPreserveRequiredSecretBackend(t *testing.T) {
	timeoutSteps := accountsCheckErrorNextSteps("load account credentials: timeout", protocol.AccountsCheckResult{
		Summary:  protocol.AccountsCheckSummary{RequiredSecretBackend: secret.BackendNative, SecretBackendOK: true},
		Accounts: []protocol.AccountCheckEvidence{{ID: "codex-test", SecretState: protocol.AccountSecretStateTimeout}},
	})
	if !containsString(timeoutSteps, "unlock or approve OS SecretStore access, then rerun: capd accounts check --json --readiness --require-secret-backend native --timeout 2m") {
		t.Fatalf("timeout steps = %+v", timeoutSteps)
	}

	quotaSteps := accountsCheckErrorNextSteps("quota is not fresh", protocol.AccountsCheckResult{
		Summary: protocol.AccountsCheckSummary{
			RequiredSecretBackend: secret.BackendNative,
			SecretBackendOK:       true,
			CheckedAccounts:       2,
			StaleQuotaAccounts:    1,
		},
	})
	if !containsString(quotaSteps, "refresh and verify daemon-side readiness with: capd accounts check --json --readiness --require-secret-backend native --timeout 2m") {
		t.Fatalf("quota steps = %+v", quotaSteps)
	}
	if got := accountsCheckFailedQuotaAccount("refresh quota: codex-test: quota: HTTP 429"); got != "codex-test" {
		t.Fatalf("failed quota account = %q", got)
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
	t.Setenv("CAPD_CODEX_AUTH_PATHS", strings.Join([]string{firstPath, secondPath}, string(os.PathListSeparator)))
	cmd.SetArgs([]string{"import", "--json"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var envResult protocol.AccountsImportResult
	if err := json.Unmarshal(out.Bytes(), &envResult); err != nil {
		t.Fatal(err)
	}
	if len(envResult.Accounts) != 2 || envResult.Accounts[0].ID != "codex-acct_first" || envResult.Accounts[1].ID != "codex-acct_second" || envResult.Account.ID != "codex-acct_second" || envResult.ImportedAccounts != 3 {
		t.Fatalf("env result = %+v", envResult)
	}
	for _, leaked := range []string{token, firstPath, secondPath, "first-access-secret", "second-access-secret", "first-refresh-secret", "second-refresh-secret", "secretRef", "secret_ref", "CODEX_HOME", filepath.Join(home, "runtimes")} {
		if strings.Contains(out.String(), leaked) {
			t.Fatalf("accounts import env leaked %q: %s", leaked, out.String())
		}
	}

	out.Reset()
	cmd = newAccountsCmd()
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
	if !strings.Contains(out.String(), "imported codex-acct_first <first@example.com>") || !strings.Contains(out.String(), "current codex-test") || !strings.Contains(out.String(), "next: verify readiness with: capd accounts check --json --readiness --timeout 2m") {
		t.Fatalf("text output = %s", out.String())
	}
	if strings.Contains(out.String(), firstPath) || strings.Contains(out.String(), "first-access-secret") || strings.Contains(out.String(), token) {
		t.Fatalf("accounts import text leaked data: %s", out.String())
	}
}

func TestAccountsImportNextStep(t *testing.T) {
	cases := map[int]string{
		0: "",
		1: daemonSecondImportNextStep(),
		2: "verify readiness with: capd accounts check --json --readiness --timeout 2m",
		3: "verify readiness with: capd accounts check --json --readiness --timeout 2m",
	}
	for imported, want := range cases {
		if got := accountsImportNextStep(imported); got != want {
			t.Fatalf("accountsImportNextStep(%d) = %q, want %q", imported, got, want)
		}
	}
}

func TestAccountsImportNextStepPreservesEnvBackend(t *testing.T) {
	t.Setenv(secret.EnvBackend, secret.BackendFile)
	want := "verify readiness with: capd accounts check --json --readiness --require-secret-backend file --timeout 2m"
	if got := accountsImportNextStep(2); got != want {
		t.Fatalf("accountsImportNextStep = %q, want %q", got, want)
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
	if !result.Summary.Ready || result.Summary.CheckedAccounts != 2 || result.Summary.MissingAccounts != 0 || result.Summary.FreshQuotaAccounts != 2 || !result.Summary.RouteDecisionOK || result.Summary.RequiredSecretBackend != secret.BackendFile || !result.Summary.SecretBackendOK {
		t.Fatalf("summary = %+v", result.Summary)
	}
	for _, row := range result.Accounts {
		if row.SecretState != protocol.AccountSecretStateReadable || !row.CredentialReadable || !row.QuotaFresh || row.QuotaState != protocol.AccountQuotaStateFresh {
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

func TestCodexAccountsQuotaAllFailurePrintsSafePartialEvidence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, secrets := seedCodexAccount(t)
	defer accounts.Close()
	ref, err := secrets.Put(context.Background(), "codex-zlow", secret.Bundle{
		Provider:     codexauth.Provider,
		AuthMode:     "chatgpt",
		AccessToken:  "zlow-access-secret",
		RefreshToken: "zlow-refresh-secret",
		AccountID:    "acct_zlow",
		RawAuthJSON:  []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"zlow-access-secret","refresh_token":"zlow-refresh-secret","secret":"raw-secret"}}`),
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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer access-secret":
			json.NewEncoder(w).Encode(map[string]any{
				"planType": "pro",
				"rateLimits": map[string]any{
					"primary": map[string]any{"usedPercent": 17},
				},
			})
		case "Bearer zlow-access-secret":
			http.Error(w, "backend-secret zlow-access-secret secretRef=file:codex-zlow", http.StatusTooManyRequests)
		default:
			http.Error(w, "unexpected auth", http.StatusUnauthorized)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "quota", "all", "--base-url", srv.URL})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "codex-zlow") || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("err = %v", err)
	}
	text := out.String()
	for _, secret := range []string{"access-secret", "refresh-secret", "zlow-access-secret", "zlow-refresh-secret", "backend-secret", "raw-secret", "secretRef", ref.String()} {
		if strings.Contains(text, secret) || strings.Contains(err.Error(), secret) {
			t.Fatalf("quota all partial failure leaked %q: err=%v out=%s", secret, err, text)
		}
	}
	var failure codexQuotaAllFailure
	if err := json.Unmarshal(out.Bytes(), &failure); err != nil {
		t.Fatalf("partial failure JSON = %q: %v", text, err)
	}
	if failure.OK || failure.FailedAccount != "codex-zlow" || !strings.Contains(failure.Error, "HTTP 429") || len(failure.Refreshed) != 1 {
		t.Fatalf("failure = %+v", failure)
	}
	if failure.Refreshed[0].ID != "codex-test" || failure.Refreshed[0].PrimaryUsedPercent != 17 || failure.Refreshed[0].QuotaState != protocol.AccountQuotaStateFresh {
		t.Fatalf("refreshed partial = %+v", failure.Refreshed)
	}
	if len(failure.NextSteps) == 0 || !strings.Contains(failure.NextSteps[0], "codex smoke --json") {
		t.Fatalf("next steps = %+v", failure.NextSteps)
	}
}

func TestCodexQuotaAllPartialFailureNextStepsPreserveSecretBackend(t *testing.T) {
	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetOut(&out)

	printCodexQuotaAllPartialFailure(cmd, nil, "codex-fail", errors.New("safe failure"), secret.BackendNative)

	var failure codexQuotaAllFailure
	if err := json.Unmarshal(out.Bytes(), &failure); err != nil {
		t.Fatalf("partial failure JSON = %q: %v", out.String(), err)
	}
	for _, want := range []string{
		"capd accounts --secret-backend native codex smoke --json",
		"capd accounts --secret-backend native codex quota all",
	} {
		if !containsSubstring(failure.NextSteps, want) {
			t.Fatalf("next steps missing %q: %+v", want, failure.NextSteps)
		}
	}
}

func containsSubstring(items []string, want string) bool {
	for _, item := range items {
		if strings.Contains(item, want) {
			return true
		}
	}
	return false
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

func TestCodexAccountsQuotaHonorsTimeout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "quota", "--base-url", srv.URL, "--timeout", "1ms"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("err = %v out=%s", err, out.String())
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

	out.Reset()
	cmd = newAccountsCmd()
	cmd.SetArgs([]string{"check", "--json", "--refresh-quota", "--require-fresh-quota", "--require-all-fresh-quota"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "codex-test") || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("json err = %v", err)
	}
	var failure accountsCheckJSONError
	if err := json.Unmarshal(out.Bytes(), &failure); err != nil {
		t.Fatalf("json error output = %q: %v", out.String(), err)
	}
	if failure.OK || len(failure.Data) == 0 || !containsString(failure.NextSteps, "fix quota refresh for failed account: codex-test") || !containsString(failure.NextSteps, "refresh and verify daemon-side readiness with: capd accounts check --json --readiness --require-secret-backend file --timeout 2m") {
		t.Fatalf("json failure = %+v", failure)
	}
	var partial protocol.AccountsCheckResult
	if err := json.Unmarshal(failure.Data, &partial); err != nil {
		t.Fatalf("accounts/check partial = %s: %v", failure.Data, err)
	}
	if partial.CheckedAccounts != 1 || !partial.QuotaRefreshed || partial.SecretBackend != secret.BackendFile {
		t.Fatalf("accounts/check partial = %+v", partial)
	}
	for _, leaked := range []string{token, "access-secret", "refresh-secret", "backend-secret", "secretRef", "secret_ref", "CODEX_HOME", filepath.Join(home, "runtimes")} {
		if strings.Contains(out.String(), leaked) {
			t.Fatalf("accounts check json refresh failure leaked %q: %s", leaked, out.String())
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
	for _, want := range []string{"PROVIDER", "SECRET_BACKEND", "QUOTA_STATE", "FRESH", "ROUTE_SCORE", "codex-test", "gemini-test", "gemini@example.com", "0.0%", secret.BackendFile, protocol.AccountQuotaStateFresh, protocol.AccountQuotaStateMissing} {
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
	if rows[0].Provider != codexauth.Provider || rows[0].ID != "codex-test" || rows[0].SecretBackend != secret.BackendFile || !rows[0].Current {
		t.Fatalf("first row = %+v", rows[0])
	}
	if rows[0].QuotaState != protocol.AccountQuotaStateFresh || !rows[0].QuotaFresh || rows[0].QuotaCheckedAt == 0 || rows[0].PrimaryUsed != "0.0%" || rows[0].RouteScore == nil || *rows[0].RouteScore != -0.01 {
		t.Fatalf("first row quota = %+v", rows[0])
	}
	if rows[1].Provider != codexauth.Provider || rows[1].ID != "codex-zlow" || rows[1].SecretBackend != secret.BackendFile || rows[1].Current {
		t.Fatalf("second row = %+v", rows[1])
	}
	if rows[1].QuotaState != protocol.AccountQuotaStateMissing || rows[1].QuotaFresh || rows[1].QuotaCheckedAt != 0 || rows[1].RouteScore == nil || *rows[1].RouteScore != 75 {
		t.Fatalf("second row quota = %+v", rows[1])
	}
	if rows[2].Provider != "gemini" || rows[2].ID != "gemini-test" {
		t.Fatalf("third row = %+v", rows[2])
	}
	if rows[2].QuotaState != protocol.AccountQuotaStateMissing || rows[2].QuotaFresh || rows[2].QuotaCheckedAt != 0 || rows[2].RouteScore != nil || rows[2].RouteReason != "" {
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
	if acc.ID != "codex-test" || !acc.SecretChecked || !acc.RuntimeChecked || !acc.ProjectionOK || !acc.RuntimeEnvOK || !acc.AuthJSONPrivate || !acc.ProjectionMarkerOK || !acc.SecretBackendOK || !acc.SecretReadable || acc.SecretState != protocol.AccountSecretStateReadable || acc.PrimaryUsed != "0.0%" || acc.PrimaryUsedPercent == nil || *acc.PrimaryUsedPercent != 0 || acc.QuotaState != protocol.AccountQuotaStateFresh || !acc.QuotaFresh || acc.QuotaCheckedAt == 0 {
		t.Fatalf("account = %+v", acc)
	}
	if result.AutoRoute == nil || result.AutoRoute.AccountID != "codex-test" {
		t.Fatalf("auto route = %+v", result.AutoRoute)
	}
}

func TestCodexAccountsSmokeJSONKeepsPartialSecretFailureEvidence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, secrets := seedCodexAccount(t)
	defer accounts.Close()
	acc, err := accounts.LoadAccount("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := secret.ParseRef(acc.SecretRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Delete(context.Background(), ref); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke", "--json"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "load secret: missing") {
		t.Fatalf("err = %v output=%s", err, out.String())
	}
	var result codexSmokeResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("json err = %v output=%s", err, out.String())
	}
	if result.OK || result.CheckedAccounts != 1 || len(result.Accounts) != 1 || !result.Accounts[0].SecretChecked || result.Accounts[0].RuntimeChecked || result.Accounts[0].SecretState != protocol.AccountSecretStateMissing || result.Accounts[0].SecretReadable {
		t.Fatalf("partial result = %+v", result)
	}
	if !containsString(result.NextSteps, "import Codex auth with: capd accounts codex import") {
		t.Fatalf("nextSteps = %+v", result.NextSteps)
	}
	for _, secret := range []string{"access-secret", "refresh-secret", "secretRef", "secret_ref", "rawAuthJson", "CODEX_HOME"} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("smoke partial failure leaked %q: %s", secret, out.String())
		}
	}
}

func TestCodexSmokeCachedAccountRowReportsSecretBackendMetadata(t *testing.T) {
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

	row := codexSmokeCachedAccountRow(accounts, acc, secret.BackendNative)
	if row.SecretChecked || row.RuntimeChecked || !row.SecretBackendOK || row.SecretReadable || row.SecretState != "" {
		t.Fatalf("native cached row = %+v", row)
	}

	row = codexSmokeCachedAccountRow(accounts, acc, secret.BackendFile)
	if row.SecretBackendOK || row.SecretState != protocol.AccountSecretStateBackendMismatch {
		t.Fatalf("mismatched cached row = %+v", row)
	}

	acc.SecretRef = secret.BackendNative + ":"
	row = codexSmokeCachedAccountRow(accounts, acc, secret.BackendNative)
	if row.SecretBackendOK || row.SecretState != protocol.AccountSecretStateMalformedRef {
		t.Fatalf("malformed cached row = %+v", row)
	}
}

func TestCodexSmokeSecretRecoveryClassifiesKeychainAccessDenied(t *testing.T) {
	if got := codexSmokeSecretErrorState(errors.New("load secret: macOS keychain status -128")); got != protocol.AccountSecretStateAccessDenied {
		t.Fatalf("state = %q", got)
	}
	step := codexSmokeSecretNextStep(protocol.AccountSecretStateAccessDenied, secret.BackendNative)
	for _, want := range []string{"approve macOS Keychain access", "capd start --secret-backend file", "capd accounts --secret-backend file codex import"} {
		if !strings.Contains(step, want) {
			t.Fatalf("step missing %q: %q", want, step)
		}
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
		if !acc.SecretChecked || !acc.RuntimeChecked || !acc.ProjectionOK || !acc.RuntimeEnvOK || !acc.AuthJSONPrivate || !acc.ProjectionMarkerOK || !acc.SecretBackendOK || !acc.SecretReadable || acc.QuotaState != protocol.AccountQuotaStateFresh || !acc.QuotaFresh {
			t.Fatalf("projection evidence missing: %+v", acc)
		}
	}
	if result.AutoRoute.AccountID != "codex-low" || result.AutoRoute.QuotaState != protocol.AccountQuotaStateFresh || !result.AutoRoute.Fresh || result.AutoRoute.Primary == nil || *result.AutoRoute.Primary != 4 {
		t.Fatalf("auto route = %+v", result.AutoRoute)
	}
	if result.AutoRoute.Reason != "auto account codex-low primary 4%" {
		t.Fatalf("auto route reason = %q", result.AutoRoute.Reason)
	}
	if result.RoutePolicy == nil || result.RoutePolicy.Name != "conservative-quota-pressure" || result.RoutePolicy.FreshTTLSeconds != int64(account.QuotaRouteCacheTTL/time.Second) {
		t.Fatalf("route policy = %+v", result.RoutePolicy)
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
	if !strings.Contains(out.String(), "next: refresh quota and rerun smoke with: capd accounts codex smoke --json --quota --require-fresh-quota --require-secret-backend file --timeout 2m") {
		t.Fatalf("next step missing quota recovery command: %s", out.String())
	}
}

func TestCodexAccountsSmokeQuotaNextStepsPreserveSecretBackend(t *testing.T) {
	if got := codexSmokeQuotaNextStep(secret.BackendFile, false); got != "refresh quota and rerun smoke with: capd accounts codex smoke --json --quota --require-fresh-quota --require-secret-backend file --timeout 2m" {
		t.Fatalf("file quota next step = %q", got)
	}
	if got := codexSmokeQuotaNextStep(secret.BackendNative, true); got != "refresh quota and rerun smoke with: capd accounts --secret-backend native codex smoke --json --quota --require-all-fresh-quota --require-secret-backend native --timeout 2m" {
		t.Fatalf("native quota next step = %q", got)
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

func TestCodexAccountsSmokeRequireMultipleReturnsPartialAccountEvidence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	staleAt := time.Now().Add(-account.QuotaRouteCacheTTL - time.Minute).Unix()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", Plan: "pro", PrimaryUsedPercent: 11, CheckedAt: staleAt}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke", "--json", "--require-multiple"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "expected multiple Codex accounts") {
		t.Fatalf("err = %v", err)
	}
	text := out.String()
	for _, leaked := range []string{"access-secret", "refresh-secret", "secretRef", "file:codex-test", "CODEX_HOME"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("partial smoke json leaked %q: %s", leaked, text)
		}
	}
	var result codexSmokeResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.OK || result.CheckedAccounts != 1 || len(result.Accounts) != 1 || result.Accounts[0].ID != "codex-test" {
		t.Fatalf("partial result = %+v", result)
	}
	if result.Accounts[0].SecretChecked || result.Accounts[0].RuntimeChecked || result.Accounts[0].SecretReadable || result.Accounts[0].ProjectionOK || result.Accounts[0].ProjectedCodexHome != "" {
		t.Fatalf("partial result should not read secret or project runtime: %+v", result.Accounts[0])
	}
	if result.Accounts[0].QuotaState != protocol.AccountQuotaStateStale || result.Accounts[0].QuotaFresh || result.Accounts[0].PrimaryUsedPercent == nil || *result.Accounts[0].PrimaryUsedPercent != 11 {
		t.Fatalf("partial account quota = %+v", result.Accounts[0])
	}
	if result.AutoRoute == nil || result.AutoRoute.AccountID != "codex-test" || result.AutoRoute.QuotaState != protocol.AccountQuotaStateStale || result.AutoRoute.Fresh {
		t.Fatalf("partial auto route = %+v", result.AutoRoute)
	}
	if result.AutoRoute.Reason != "auto account codex-test without fresh cached quota; current account tie-break" {
		t.Fatalf("partial auto route reason = %q", result.AutoRoute.Reason)
	}
	if len(result.RouteCandidates) != 1 || result.RouteCandidates[0].AccountID != "codex-test" || result.RouteCandidates[0].Reason != "auto account codex-test without fresh cached quota; current account tie-break" {
		t.Fatalf("partial route candidates = %+v", result.RouteCandidates)
	}
	if !containsString(result.NextSteps, codexLocalImportNextStep(secret.BackendFile, true)) {
		t.Fatalf("next steps = %+v", result.NextSteps)
	}
	if !containsString(result.NextSteps, "refresh quota and rerun smoke with: capd accounts codex smoke --json --quota --require-fresh-quota --require-secret-backend file --timeout 2m") {
		t.Fatalf("next steps = %+v", result.NextSteps)
	}
}

func TestCodexAccountsSmokeFailureNextStepsIncludeBackendMismatchEvidence(t *testing.T) {
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
	cmd.SetArgs([]string{"codex", "smoke", "--json", "--require-multiple", "--require-secret-backend", "file"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "expected multiple Codex accounts") {
		t.Fatalf("err = %v", err)
	}
	var result codexSmokeResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("json err = %v output=%s", err, out.String())
	}
	want := "rerun smoke with the required SecretStore backend: capd accounts --secret-backend native codex smoke --json --require-secret-backend native --timeout 2m, or re-import the account with the active backend: capd accounts codex import --auth /path/to/auth.json"
	if !containsString(result.NextSteps, want) {
		t.Fatalf("next steps missing backend repair %q: %+v", want, result.NextSteps)
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
	if result.AutoRoute.Reason != "auto account codex-test without fresh cached quota; current account tie-break" {
		t.Fatalf("auto route reason = %q", result.AutoRoute.Reason)
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
		"SECRET_STATE",
		"CREDENTIAL",
		"RUNTIME",
		"AUTH_JSON",
		"MARKER",
		"QUOTA",
		"FRESH",
		protocol.AccountSecretStateReadable,
		protocol.AccountQuotaStateFresh,
		"true",
		"9.0%",
		"auto route: codex-test quota fresh fresh true secret file primary 9.0% score ",
		"route candidates:",
		"codex-test quota fresh fresh true secret file primary 9.0% score ",
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
	if !strings.Contains(out.String(), "refresh quota and rerun smoke with: capd accounts codex smoke --json --quota --require-all-fresh-quota --require-secret-backend file --timeout 2m") {
		t.Fatalf("next step missing quota recovery command: %s", out.String())
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
	if !containsString(got.NextSteps, codexLocalImportNextStep(secret.BackendFile, true)) {
		t.Fatalf("nextSteps = %+v", got.NextSteps)
	}
	if got.AutoRoute == nil || got.AutoRoute.AccountID != "codex-test" || !got.AutoRoute.Fresh || got.AutoRoute.Primary == nil || *got.AutoRoute.Primary != 4 {
		t.Fatalf("preflight failure should keep auto-route evidence: %+v", got)
	}
	if got.AutoRoute.Reason != "auto account codex-test primary 4%; current account tie-break" {
		t.Fatalf("preflight failure auto-route reason = %q", got.AutoRoute.Reason)
	}
	if len(got.RouteCandidates) != 1 || got.RouteCandidates[0].AccountID != "codex-test" || !got.RouteCandidates[0].Fresh {
		t.Fatalf("preflight failure should keep route candidates: %+v", got.RouteCandidates)
	}
	if len(got.Accounts) != 1 || got.Accounts[0].ID != "codex-test" || !got.Accounts[0].QuotaFresh || got.Accounts[0].SecretChecked || got.Accounts[0].RuntimeChecked || got.Accounts[0].SecretReadable || got.Accounts[0].ProjectionOK || got.Accounts[0].ProjectedCodexHome != "" {
		t.Fatalf("preflight failure should keep cached account evidence without secret/runtime projection: %+v", got)
	}
	for _, secret := range []string{"access-secret", "refresh-secret", "secretRef", "secret_ref", "rawAuthJson"} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("smoke failure JSON leaked %q: %s", secret, out.String())
		}
	}
}

func TestCodexAccountsSmokeNativeImportNextStep(t *testing.T) {
	if got := codexLocalImportNextStep(secret.BackendNative, false); got != "import Codex auth with: capd accounts --secret-backend native codex import" {
		t.Fatalf("import next step = %q", got)
	}
	if got := codexLocalImportNextStep(secret.BackendNative, true); got != "import a second Codex account with: capd accounts --secret-backend native codex import --auth /path/to/auth.json, or batch import with: "+codexAuthPathsEnvExample("capd accounts --secret-backend native codex import") {
		t.Fatalf("second import next step = %q", got)
	}
	if got := codexLocalImportNextStep(secret.BackendFile, true); got != "import a second Codex account with: capd accounts codex import --auth /path/to/auth.json, or batch import with: "+codexAuthPathsEnvExample("capd accounts codex import") {
		t.Fatalf("file import next step = %q", got)
	}
}

func TestCodexAccountsSmokeSecretNextStepsPreserveSecretBackend(t *testing.T) {
	if got := codexSmokeSecretNextStep(protocol.AccountSecretStateTimeout, secret.BackendNative); got != "unlock or approve OS SecretStore access, then rerun: capd accounts --secret-backend native codex smoke --json --timeout 2m" {
		t.Fatalf("timeout next step = %q", got)
	}
	if got := codexSmokeSecretNextStep(protocol.AccountSecretStateUnreadable, secret.BackendNative); got != "re-import the failing Codex account with: capd accounts --secret-backend native codex import --auth /path/to/auth.json" {
		t.Fatalf("unreadable next step = %q", got)
	}
	if got := codexSmokeSecretNextStep(protocol.AccountSecretStateTimeout, secret.BackendFile); got != "unlock or approve OS SecretStore access, then rerun: capd accounts codex smoke --json --timeout 2m" {
		t.Fatalf("file timeout next step = %q", got)
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
	wantNext := "next: rerun smoke with the required SecretStore backend: capd accounts --secret-backend native codex smoke --json --require-secret-backend native --timeout 2m"
	if !strings.Contains(out.String(), wantNext) {
		t.Fatalf("output missing next step %q: %s", wantNext, out.String())
	}
}

func TestCodexAccountsSmokeBackendMismatchNextSteps(t *testing.T) {
	if got := codexSmokeBackendMismatchNextStep(secret.BackendNative); got != "rerun smoke with the required SecretStore backend: capd accounts --secret-backend native codex smoke --json --require-secret-backend native --timeout 2m" {
		t.Fatalf("required backend next step = %q", got)
	}
	if got := codexSmokeBackendMismatchNextStep(secret.BackendFile); got != "rerun smoke with the required SecretStore backend: capd accounts codex smoke --json --require-secret-backend file --timeout 2m" {
		t.Fatalf("file backend next step = %q", got)
	}
	want := "rerun smoke with the required SecretStore backend: capd accounts --secret-backend native codex smoke --json --require-secret-backend native --timeout 2m, or re-import the account with the active backend: capd accounts codex import --auth /path/to/auth.json"
	if got := codexSmokeAccountBackendMismatchNextStep(secret.BackendNative, secret.BackendFile); got != want {
		t.Fatalf("account backend next step = %q", got)
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
	if err == nil || !strings.Contains(err.Error(), "secret backend mismatch") {
		t.Fatalf("err = %v", err)
	}
	for _, leaked := range []string{"access-secret", "refresh-secret", "native:codex-test"} {
		if strings.Contains(err.Error(), leaked) || strings.Contains(out.String(), leaked) {
			t.Fatalf("smoke leaked %q: err=%v out=%s", leaked, err, out.String())
		}
	}

	out.Reset()
	cmd = newAccountsCmd()
	cmd.SetArgs([]string{"codex", "smoke", "--json"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "secret backend mismatch") {
		t.Fatalf("json err = %v", err)
	}
	var result codexSmokeResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("json output = %q: %v", out.String(), err)
	}
	if result.OK || len(result.Accounts) != 1 || result.Accounts[0].SecretState != protocol.AccountSecretStateBackendMismatch || result.Accounts[0].SecretBackendOK {
		t.Fatalf("partial result = %+v", result)
	}
	wantNext := "rerun smoke with the required SecretStore backend: capd accounts --secret-backend native codex smoke --json --require-secret-backend native --timeout 2m, or re-import the account with the active backend: capd accounts codex import --auth /path/to/auth.json"
	if len(result.NextSteps) == 0 || result.NextSteps[0] != wantNext {
		t.Fatalf("nextSteps = %+v", result.NextSteps)
	}
	for _, leaked := range []string{"access-secret", "refresh-secret", "native:codex-test", "secretRef", "CODEX_HOME"} {
		if strings.Contains(out.String(), leaked) {
			t.Fatalf("smoke json leaked %q: %s", leaked, out.String())
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

type memorySecretStore struct {
	backend string
	bundles map[string]secret.Bundle
}

func newMemorySecretStore(backend string) *memorySecretStore {
	return &memorySecretStore{backend: backend, bundles: map[string]secret.Bundle{}}
}

func (st *memorySecretStore) Backend() string { return st.backend }

func (st *memorySecretStore) Put(_ context.Context, id string, bundle secret.Bundle) (secret.Ref, error) {
	if id == "" {
		return secret.Ref{}, fmt.Errorf("secret id is required")
	}
	st.bundles[id] = bundle
	return secret.Ref{Backend: st.backend, ID: id}, nil
}

func (st *memorySecretStore) Get(_ context.Context, ref secret.Ref) (secret.Bundle, error) {
	if ref.Backend != "" && ref.Backend != st.backend {
		return secret.Bundle{}, fmt.Errorf("secret backend %q is not %q", ref.Backend, st.backend)
	}
	bundle, ok := st.bundles[ref.ID]
	if !ok {
		return secret.Bundle{}, os.ErrNotExist
	}
	return bundle, nil
}

func (st *memorySecretStore) Delete(_ context.Context, ref secret.Ref) error {
	if ref.Backend != "" && ref.Backend != st.backend {
		return fmt.Errorf("secret backend %q is not %q", ref.Backend, st.backend)
	}
	delete(st.bundles, ref.ID)
	return nil
}

type unreadableSecretStore struct {
	*memorySecretStore
}

func (st *unreadableSecretStore) Get(context.Context, secret.Ref) (secret.Bundle, error) {
	return secret.Bundle{}, os.ErrPermission
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

func containsRepairPlanStep(steps []protocol.RepairStep, id string) bool {
	for _, step := range steps {
		if step.ID == id {
			return true
		}
	}
	return false
}
