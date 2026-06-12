package codexauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/secret"
)

func TestImportAuthJSONStoresSecretsOutOfSQLite(t *testing.T) {
	dir := t.TempDir()
	idToken := jwt(t, map[string]any{"email": "dev@example.com", "account_id": "acct_123456"})
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{
		"tokens": {
			"access_token": "access-secret",
			"refresh_token": "refresh-secret",
			"id_token": "`+idToken+`"
		}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	accountDB := filepath.Join(dir, "accounts.db")
	accounts, err := account.OpenStore(accountDB)
	if err != nil {
		t.Fatal(err)
	}
	defer accounts.Close()
	secrets := secret.NewFileStore(filepath.Join(dir, "secrets"))

	result, err := Importer{Accounts: accounts, Secrets: secrets}.ImportAuthJSON(context.Background(), authPath)
	if err != nil {
		t.Fatal(err)
	}
	if result.Account.Email != "dev@example.com" || result.Account.AccountID != "acct_123456" {
		t.Fatalf("account = %+v", result.Account)
	}
	if !strings.HasPrefix(result.Account.SecretRef, "file:") {
		t.Fatalf("secret ref = %q", result.Account.SecretRef)
	}
	bundle, err := secrets.Get(context.Background(), result.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.AccessToken != "access-secret" || bundle.RefreshToken != "refresh-secret" {
		t.Fatalf("bundle = %+v", bundle)
	}

	dbBytes, err := os.ReadFile(accountDB)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(dbBytes), "access-secret") || strings.Contains(string(dbBytes), "refresh-secret") {
		t.Fatal("SQLite database contains token material")
	}
}

func TestImportAuthJSONRejectsNoToken(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"email":"dev@example.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	accounts, err := account.OpenStore(filepath.Join(dir, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer accounts.Close()
	_, err = Importer{
		Accounts: accounts,
		Secrets:  secret.NewFileStore(filepath.Join(dir, "secrets")),
	}.ImportAuthJSON(context.Background(), authPath)
	if err == nil {
		t.Fatal("expected missing-token error")
	}
}

func TestImportAuthJSONRejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, []byte(strings.Repeat(" ", maxAuthJSONBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	accounts, err := account.OpenStore(filepath.Join(dir, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer accounts.Close()

	_, err = Importer{
		Accounts: accounts,
		Secrets:  secret.NewFileStore(filepath.Join(dir, "secrets")),
	}.ImportAuthJSON(context.Background(), authPath)
	if err == nil || !strings.Contains(err.Error(), "codex auth json exceeded") {
		t.Fatalf("err = %v", err)
	}
	list, err := accounts.ListAccounts(Provider)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("accounts = %+v", list)
	}
}

func TestSafeImportErrorRedactsPathsAndTokenMarkers(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	pathMsg := SafeImportError(&os.PathError{Op: "open", Path: authPath, Err: os.ErrNotExist}, authPath)
	if pathMsg != "read auth json failed" || strings.Contains(pathMsg, authPath) {
		t.Fatalf("pathMsg = %q", pathMsg)
	}

	tokenMsg := SafeImportError(fmt.Errorf("backend rejected access_token sk-sensitive"), authPath)
	if tokenMsg != "import auth json failed" || strings.Contains(tokenMsg, "access_token") || strings.Contains(tokenMsg, "sk-sensitive") {
		t.Fatalf("tokenMsg = %q", tokenMsg)
	}

	fieldMsg := SafeImportError(fmt.Errorf("codex auth json did not contain a supported token field"), authPath)
	if !strings.Contains(fieldMsg, "supported token field") {
		t.Fatalf("fieldMsg = %q", fieldMsg)
	}
}

func jwt(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]any{"alg": "none"})
	payload, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "."
}
