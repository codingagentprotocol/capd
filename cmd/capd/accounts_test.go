package main

import (
	"bytes"
	"context"
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
