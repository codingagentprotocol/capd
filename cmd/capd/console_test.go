package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/codingagentprotocol/capd/internal/security"
)

func TestConsoleCmdPrintsProbeURLAfterHealthCheck(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-console-secret"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	healthz := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		healthz++
		w.Write([]byte("ok\n"))
	}))
	defer ts.Close()
	host, port := splitTestURL(t, ts.URL)
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)

	var out bytes.Buffer
	cmd := newConsoleCmd()
	cmd.SetArgs([]string{"--probe", "--url"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(out.String())
	if healthz != 1 {
		t.Fatalf("healthz calls = %d", healthz)
	}
	if !strings.Contains(text, "/probe/?") || strings.Contains(text, "token="+token) {
		t.Fatalf("probe URL = %q", text)
	}
	assertScopedURLToken(t, text, token, security.TokenScopeProbeRead)
}

func TestConsoleCmdPrintsRequiredSecretBackendURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-console-native"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Write([]byte("ok\n"))
	}))
	defer ts.Close()
	host, port := splitTestURL(t, ts.URL)
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)

	var out bytes.Buffer
	cmd := newConsoleCmd()
	cmd.SetArgs([]string{"--probe", "--url", "--require-secret-backend", "native"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(out.String())
	if !strings.Contains(text, "/probe/?") || strings.Contains(text, "token="+token) || !strings.Contains(text, "requireSecretBackend=native") {
		t.Fatalf("probe URL = %q", text)
	}
	assertScopedURLToken(t, text, token, security.TokenScopeProbeRead)
}

func TestConsoleCmdRejectsUnknownRequiredSecretBackend(t *testing.T) {
	cmd := newConsoleCmd()
	cmd.SetArgs([]string{"--url", "--require-secret-backend", "mystery"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `unknown secret backend "mystery"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestConsoleCmdFailsBeforePrintingURLWhenDaemonUnhealthy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-console-unhealthy"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newConsoleCmd()
	cmd.SetArgs([]string{"--url"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "capd start") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(out.String(), token) {
		t.Fatalf("printed token on failure: %s", out.String())
	}
}

func TestLabelPath(t *testing.T) {
	if got := labelPath(false); got != "console/" {
		t.Fatalf("console path = %q", got)
	}
	if got := labelPath(true); got != "probe/" {
		t.Fatalf("probe path = %q", got)
	}
}

func assertScopedURLToken(t *testing.T, rawURL, rootToken, wantScope string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	scoped := u.Query().Get("token")
	if scoped == "" || scoped == rootToken {
		t.Fatalf("scoped token = %q", scoped)
	}
	scope, ok := security.VerifyScopedToken(rootToken, scoped, time.Now())
	if !ok || scope != wantScope {
		t.Fatalf("scope=%q ok=%t token=%q", scope, ok, scoped)
	}
}
