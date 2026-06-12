package codexauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/secret"
)

const Provider = "codex"

const maxAuthJSONBytes = 1 << 20

type Importer struct {
	Accounts *account.Store
	Secrets  secret.Store
}

type ImportResult struct {
	Account account.Account
	Secret  secret.Ref
}

func DefaultAuthPath(codexHome string) (string, error) {
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		codexHome = filepath.Join(home, ".codex")
	}
	return filepath.Join(codexHome, "auth.json"), nil
}

func (im Importer) ImportAuthJSON(ctx context.Context, path string) (ImportResult, error) {
	if im.Accounts == nil {
		return ImportResult{}, fmt.Errorf("account store is required")
	}
	if im.Secrets == nil {
		return ImportResult{}, fmt.Errorf("secret store is required")
	}
	data, err := readAuthJSON(path)
	if err != nil {
		return ImportResult{}, err
	}
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return ImportResult{}, fmt.Errorf("parse codex auth json: %w", err)
	}

	bundle := bundleFromAuthJSON(root, data)
	if bundle.AccessToken == "" && bundle.RefreshToken == "" && bundle.IDToken == "" && bundle.APIKey == "" {
		return ImportResult{}, fmt.Errorf("codex auth json did not contain a supported token field")
	}

	id := accountID(bundle)
	ref, err := im.Secrets.Put(ctx, id, bundle)
	if err != nil {
		return ImportResult{}, err
	}
	acc := account.Account{
		ID:        id,
		Provider:  Provider,
		AuthMode:  bundle.AuthMode,
		Email:     bundle.Email,
		AccountID: bundle.AccountID,
		SecretRef: ref.String(),
	}
	if err := im.Accounts.UpsertAccount(acc); err != nil {
		return ImportResult{}, err
	}
	if current, err := im.Accounts.CurrentAccount(Provider); err != nil {
		return ImportResult{}, err
	} else if current == "" {
		if err := im.Accounts.SetCurrentAccount(Provider, acc.ID); err != nil {
			return ImportResult{}, err
		}
	}
	return ImportResult{Account: acc, Secret: ref}, nil
}

func SafeImportError(err error, authPath string) string {
	if err == nil {
		return ""
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return "read auth json failed"
	}
	msg := err.Error()
	if authPath != "" && strings.Contains(msg, authPath) {
		return "read auth json failed"
	}
	if containsSensitiveAuthErrorText(msg) {
		return "import auth json failed"
	}
	return msg
}

func containsSensitiveAuthErrorText(msg string) bool {
	lower := strings.ToLower(msg)
	for _, marker := range []string{
		"access_token",
		"refreshtoken",
		"refresh_token",
		"idtoken",
		"id_token",
		"openai_api_key",
		"api_key",
		"authorization:",
		"bearer ",
		"rawauthjson",
		"raw_auth_json",
		"sk-",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func readAuthJSON(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxAuthJSONBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAuthJSONBytes {
		return nil, fmt.Errorf("codex auth json exceeded %d bytes", maxAuthJSONBytes)
	}
	return data, nil
}

func bundleFromAuthJSON(root any, data []byte) secret.Bundle {
	fields := map[string]string{}
	collectStrings(root, "", fields)
	bundle := secret.Bundle{
		Provider:     Provider,
		AuthMode:     first(fields, "auth_mode", "authMode"),
		AccessToken:  first(fields, "access_token", "accessToken"),
		RefreshToken: first(fields, "refresh_token", "refreshToken"),
		IDToken:      first(fields, "id_token", "idToken"),
		APIKey:       first(fields, "openai_api_key", "OPENAI_API_KEY", "api_key", "apiKey"),
		AccountID:    first(fields, "account_id", "accountId", "chatgpt_account_id", "chatgptAccountId"),
		Email:        first(fields, "email"),
		RawAuthJSON:  append(json.RawMessage(nil), data...),
	}
	if bundle.AuthMode == "" {
		bundle.AuthMode = "oauth"
	}
	if bundle.APIKey != "" && bundle.AccessToken == "" && bundle.RefreshToken == "" && bundle.IDToken == "" {
		bundle.AuthMode = "api_key"
	}
	if bundle.IDToken != "" {
		claims := parseJWTClaims(bundle.IDToken)
		if bundle.Email == "" {
			bundle.Email = stringClaim(claims, "email")
		}
		if bundle.AccountID == "" {
			bundle.AccountID = firstClaim(claims, "account_id", "accountId", "https://api.openai.com/auth/account_id")
		}
	}
	return bundle
}

func collectStrings(v any, key string, out map[string]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			collectStrings(child, k, out)
		}
	case []any:
		for _, child := range x {
			collectStrings(child, key, out)
		}
	case string:
		if key != "" && x != "" {
			out[normalizeKey(key)] = x
		}
	}
}

func normalizeKey(key string) string {
	key = strings.TrimSpace(key)
	key = strings.ReplaceAll(key, "-", "_")
	return strings.ToLower(key)
}

func first(fields map[string]string, keys ...string) string {
	for _, key := range keys {
		if v := fields[normalizeKey(key)]; v != "" {
			return v
		}
	}
	return ""
}

func parseJWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(data, &claims) != nil {
		return nil
	}
	return claims
}

func firstClaim(claims map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := stringClaim(claims, key); v != "" {
			return v
		}
	}
	return ""
}

func stringClaim(claims map[string]any, key string) string {
	if claims == nil {
		return ""
	}
	if v, ok := claims[key].(string); ok {
		return v
	}
	return ""
}

func accountID(bundle secret.Bundle) string {
	switch {
	case bundle.AccountID != "":
		return "codex-" + safeID(bundle.AccountID)
	case bundle.Email != "":
		return "codex-" + safeID(bundle.Email)
	case bundle.AccessToken != "":
		return "codex-" + shortHash(bundle.AccessToken)
	case bundle.APIKey != "":
		return "codex-" + shortHash(bundle.APIKey)
	default:
		return "codex-account"
	}
}

func safeID(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return shortHash(value)
	}
	return out
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}
