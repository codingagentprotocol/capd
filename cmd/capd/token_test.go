package main

import (
	"bytes"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/codingagentprotocol/capd/internal/security"
)

func TestTokenURLUsesScopedConsoleToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rootToken := "tok-root-console"
	if err := writeTokenForTest(home, rootToken); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newTokenCmd()
	cmd.SetArgs([]string{"--url"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	rawURL := strings.TrimSpace(out.String())
	if strings.Contains(rawURL, rootToken) {
		t.Fatalf("url leaked root token: %s", rawURL)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	scoped := u.Query().Get("token")
	scope, ok := security.VerifyScopedToken(rootToken, scoped, time.Now())
	if !ok || scope != security.TokenScopeConsole {
		t.Fatalf("scope=%q ok=%t url=%s", scope, ok, rawURL)
	}
}
