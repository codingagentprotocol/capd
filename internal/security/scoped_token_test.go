package security

import (
	"strings"
	"testing"
	"time"
)

func TestScopedTokenRoundTripAndExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	token, err := MintScopedToken("root-secret", TokenScopeProbeRead, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(token, "root-secret") || strings.Contains(token, TokenScopeProbeRead) {
		t.Fatalf("scoped token leaked material: %s", token)
	}
	scope, ok := VerifyScopedToken("root-secret", token, now.Add(30*time.Minute))
	if !ok || scope != TokenScopeProbeRead {
		t.Fatalf("scope=%q ok=%t", scope, ok)
	}
	if _, ok := VerifyScopedToken("root-secret", token, now.Add(2*time.Hour)); ok {
		t.Fatal("expired token verified")
	}
	if _, ok := VerifyScopedToken("other-root", token, now.Add(30*time.Minute)); ok {
		t.Fatal("wrong root token verified")
	}
}

func TestRawDaemonTokenVerifiesAsFullScope(t *testing.T) {
	scope, ok := VerifyScopedToken("root-secret", "root-secret", time.Now())
	if !ok || scope != TokenScopeFull {
		t.Fatalf("scope=%q ok=%t", scope, ok)
	}
}

func TestScopedTokenRejectsUnknownScope(t *testing.T) {
	if _, err := MintScopedToken("root-secret", "accounts:write", time.Hour, time.Now()); err == nil {
		t.Fatal("unknown scope accepted")
	}
}
