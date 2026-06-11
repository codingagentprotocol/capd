package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

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
	if !result.OK || result.CheckedAccounts != 1 || len(result.Accounts) != 1 {
		t.Fatalf("result = %+v", result)
	}
	acc := result.Accounts[0]
	if acc.ID != "codex-test" || !acc.ProjectionOK || acc.PrimaryUsed != "0.0%" || acc.PrimaryUsedPercent == nil || *acc.PrimaryUsedPercent != 0 {
		t.Fatalf("account = %+v", acc)
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
