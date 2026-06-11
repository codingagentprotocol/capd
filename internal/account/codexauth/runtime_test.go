package codexauth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/secret"
)

func TestRuntimeProjectorWritesIsolatedCodexHome(t *testing.T) {
	dir := t.TempDir()
	secrets := secret.NewFileStore(filepath.Join(dir, "secrets"))
	ref, err := secrets.Put(context.Background(), "codex-acct", secret.Bundle{
		Provider:    Provider,
		AuthMode:    "oauth",
		AccessToken: "access-secret",
		RawAuthJSON: []byte(`{"tokens":{"access_token":"access-secret"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	acc := account.Account{ID: "codex-acct", Provider: Provider, SecretRef: ref.String()}

	profile, err := RuntimeProjector{
		Root:    filepath.Join(dir, "runtimes"),
		Secrets: secrets,
	}.Project(context.Background(), acc)
	if err != nil {
		t.Fatal(err)
	}
	if profile.CodexHome == "" || profile.Env[0] != "CODEX_HOME="+profile.CodexHome {
		t.Fatalf("profile = %+v", profile)
	}
	authPath := filepath.Join(profile.CodexHome, "auth.json")
	authBytes, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	var auth struct {
		Tokens map[string]string `json:"tokens"`
	}
	if err := json.Unmarshal(authBytes, &auth); err != nil {
		t.Fatal(err)
	}
	if auth.Tokens["access_token"] != "access-secret" {
		t.Fatalf("auth json = %q", authBytes)
	}
	info, err := os.Stat(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("auth mode = %o", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(profile.CodexHome, ".capd_projection.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeProjectorSanitizesAccountDirectory(t *testing.T) {
	dir := t.TempDir()
	secrets := secret.NewFileStore(filepath.Join(dir, "secrets"))
	ref, err := secrets.Put(context.Background(), "codex-acct", secret.Bundle{
		Provider:    Provider,
		AuthMode:    "oauth",
		AccessToken: "access-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	acc := account.Account{ID: "../outside", Provider: Provider, SecretRef: ref.String()}
	profile, err := RuntimeProjector{
		Root:    filepath.Join(dir, "runtimes"),
		Secrets: secrets,
	}.Project(context.Background(), acc)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := filepath.Join(dir, "runtimes", Provider) + string(filepath.Separator)
	if !strings.HasPrefix(profile.CodexHome, wantPrefix) {
		t.Fatalf("CodexHome escaped runtime root: %q", profile.CodexHome)
	}
}

func TestRuntimeProjectorSyncsRefreshedProjectedAuth(t *testing.T) {
	dir := t.TempDir()
	secrets := secret.NewFileStore(filepath.Join(dir, "secrets"))
	ref, err := secrets.Put(context.Background(), "codex-acct", secret.Bundle{
		Provider:     Provider,
		AuthMode:     "chatgpt",
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		RawAuthJSON:  []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"old-access","refresh_token":"old-refresh"},"last_refresh":"2026-06-01T00:00:00Z"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	acc := account.Account{ID: "codex-acct", Provider: Provider, SecretRef: ref.String(), AccountID: "acct_1"}
	projector := RuntimeProjector{
		Root:    filepath.Join(dir, "runtimes"),
		Secrets: secrets,
	}
	profile, err := projector.Project(context.Background(), acc)
	if err != nil {
		t.Fatal(err)
	}
	refreshed := []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"new-access","refresh_token":"new-refresh","account_id":"acct_1"},"last_refresh":"2026-06-10T00:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(profile.CodexHome, "auth.json"), refreshed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := projector.Project(context.Background(), acc); err != nil {
		t.Fatal(err)
	}
	bundle, err := secrets.Get(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.AccessToken != "new-access" || bundle.RefreshToken != "new-refresh" {
		t.Fatalf("bundle = %+v", bundle)
	}
	if !strings.Contains(string(bundle.RawAuthJSON), "new-access") {
		t.Fatalf("raw auth was not refreshed: %s", bundle.RawAuthJSON)
	}
}
