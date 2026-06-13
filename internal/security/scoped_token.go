package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	TokenScopeFull        = "full"
	TokenScopeConsole     = "console"
	TokenScopeConsoleRead = "console:read"
	TokenScopeProbeRead   = "probe:read"
)

const scopedTokenVersion = "capd1"

// MintScopedToken derives a short-lived, signed token from the daemon token.
// The daemon token itself is never embedded in the scoped token.
func MintScopedToken(rootToken, scope string, ttl time.Duration, now time.Time) (string, error) {
	scope = strings.TrimSpace(scope)
	if err := validateTokenScope(scope); err != nil {
		return "", err
	}
	if strings.TrimSpace(rootToken) == "" {
		return "", fmt.Errorf("root token is empty")
	}
	if ttl <= 0 {
		return "", fmt.Errorf("ttl must be positive")
	}
	exp := now.Add(ttl).Unix()
	scopePart := base64.RawURLEncoding.EncodeToString([]byte(scope))
	payload := scopedTokenPayload(scopePart, exp)
	sig := scopedTokenSignature(rootToken, payload)
	return strings.Join([]string{scopedTokenVersion, scopePart, strconv.FormatInt(exp, 10), sig}, "."), nil
}

// VerifyScopedToken returns the token scope. The raw daemon token remains a
// backward-compatible full-scope token.
func VerifyScopedToken(rootToken, token string, now time.Time) (string, bool) {
	if hmac.Equal([]byte(token), []byte(rootToken)) {
		return TokenScopeFull, true
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != scopedTokenVersion {
		return "", false
	}
	scopeBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	scope := string(scopeBytes)
	if err := validateTokenScope(scope); err != nil {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || exp <= 0 || now.Unix() > exp {
		return "", false
	}
	payload := scopedTokenPayload(parts[1], exp)
	want := scopedTokenSignature(rootToken, payload)
	if !hmac.Equal([]byte(parts[3]), []byte(want)) {
		return "", false
	}
	return scope, true
}

func validateTokenScope(scope string) error {
	switch scope {
	case TokenScopeFull, TokenScopeConsole, TokenScopeConsoleRead, TokenScopeProbeRead:
		return nil
	default:
		return fmt.Errorf("unknown token scope %q", scope)
	}
}

func scopedTokenPayload(scopePart string, exp int64) string {
	return scopedTokenVersion + "\n" + scopePart + "\n" + strconv.FormatInt(exp, 10)
}

func scopedTokenSignature(rootToken, payload string) string {
	mac := hmac.New(sha256.New, []byte(rootToken))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
