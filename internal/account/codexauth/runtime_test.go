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

func TestRuntimeProjectorTightensExistingCodexHomePermissions(t *testing.T) {
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
	root := filepath.Join(dir, "runtimes")
	codexHome := filepath.Join(root, Provider, "codex-acct")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	acc := account.Account{ID: "codex-acct", Provider: Provider, SecretRef: ref.String()}

	profile, err := RuntimeProjector{Root: root, Secrets: secrets}.Project(context.Background(), acc)
	if err != nil {
		t.Fatal(err)
	}
	if profile.CodexHome != codexHome {
		t.Fatalf("CodexHome = %q, want %q", profile.CodexHome, codexHome)
	}
	assertMode(t, codexHome, 0o700)
	assertMode(t, filepath.Join(codexHome, "auth.json"), 0o600)
}

func TestVerifyRuntimeProfileReturnsSafeEvidence(t *testing.T) {
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
	profile, err := RuntimeProjector{
		Root:    filepath.Join(dir, "runtimes"),
		Secrets: secrets,
	}.Project(context.Background(), account.Account{ID: "codex-acct", Provider: Provider, SecretRef: ref.String()})
	if err != nil {
		t.Fatal(err)
	}

	evidence, err := VerifyRuntimeProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.RuntimeEnvOK || !evidence.AuthJSONPrivate || !evidence.ProjectionMarkerOK {
		t.Fatalf("evidence = %+v", evidence)
	}

	badProfile := profile
	badProfile.Env = nil
	_, err = VerifyRuntimeProfile(badProfile)
	if err == nil || !strings.Contains(err.Error(), "runtime env missing CODEX_HOME") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), profile.CodexHome) || strings.Contains(err.Error(), "access-secret") {
		t.Fatalf("verification error leaked sensitive details: %v", err)
	}
}

func TestRemoveRuntimeProjectionDeletesOnlyMatchingCapdProjection(t *testing.T) {
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
	acc := account.Account{ID: "codex-acct", Provider: Provider, SecretRef: ref.String()}
	projector := RuntimeProjector{Root: filepath.Join(dir, "runtimes"), Secrets: secrets}
	profile, err := projector.Project(context.Background(), acc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(profile.CodexHome, "auth.json")); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveRuntimeProjection(projector.Root, acc)
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("projection was not removed")
	}
	if _, err := os.Stat(profile.CodexHome); !os.IsNotExist(err) {
		t.Fatalf("projection still exists err=%v", err)
	}
	removed, err = RemoveRuntimeProjection(projector.Root, acc)
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("missing projection reported as removed")
	}
}

func TestRemoveRuntimeProjectionRejectsMarkerMismatch(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "runtimes")
	codexHome := filepath.Join(root, Provider, "codex-acct")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte("token-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	badMarker := []byte(`{"managedBy":"someone-else","provider":"codex","account":"codex-acct"}`)
	if err := os.WriteFile(filepath.Join(codexHome, ".capd_projection.json"), badMarker, 0o600); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveRuntimeProjection(root, account.Account{ID: "codex-acct", Provider: Provider})
	if err == nil || !strings.Contains(err.Error(), "runtime projection marker mismatch") {
		t.Fatalf("err = %v", err)
	}
	if removed {
		t.Fatal("mismatched projection reported as removed")
	}
	if _, err := os.Stat(filepath.Join(codexHome, "auth.json")); err != nil {
		t.Fatalf("projection should not be removed: %v", err)
	}
	if strings.Contains(err.Error(), "token-secret") {
		t.Fatalf("error leaked token material: %v", err)
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

func TestRuntimeProjectorRejectsSecretBackendMismatch(t *testing.T) {
	dir := t.TempDir()
	secrets := secret.NewFileStore(filepath.Join(dir, "secrets"))
	acc := account.Account{ID: "codex-acct", Provider: Provider, SecretRef: secret.BackendNative + ":codex-acct"}
	_, err := RuntimeProjector{
		Root:    filepath.Join(dir, "runtimes"),
		Secrets: secrets,
	}.Project(context.Background(), acc)
	if err == nil || !strings.Contains(err.Error(), `secret backend = "native", active backend = "file"`) {
		t.Fatalf("err = %v", err)
	}
	for _, leaked := range []string{"codex-acct", "access-secret", "refresh-secret"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("projector error leaked %q: %v", leaked, err)
		}
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

func TestRuntimeProjectorIgnoresOversizedProjectedAuth(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(profile.CodexHome, "auth.json"), []byte(strings.Repeat(" ", maxAuthJSONBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := projector.Project(context.Background(), acc); err != nil {
		t.Fatal(err)
	}

	authBytes, err := os.ReadFile(filepath.Join(profile.CodexHome, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(authBytes) > maxAuthJSONBytes || !strings.Contains(string(authBytes), "old-access") {
		t.Fatalf("projected auth was not restored from SecretStore: len=%d body=%q", len(authBytes), authBytes)
	}
	bundle, err := secrets.Get(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.AccessToken != "old-access" || strings.Contains(string(bundle.RawAuthJSON), strings.Repeat(" ", 32)) {
		t.Fatalf("bundle = %+v", bundle)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
