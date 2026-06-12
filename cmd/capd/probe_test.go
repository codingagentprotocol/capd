package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeDataCmdUsesAuthorizationHeader(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-probe-secret"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	var sawAuth, rawQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/probe/data" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		sawAuth = r.Header.Get("Authorization")
		rawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"health":{"version":"test","protocolVersion":"0.1","secretBackend":"native"},"accountsCheck":{"provider":"codex","secretBackend":"native","checkedAccounts":2},"autoRoute":{"accountId":"codex-a","quotaState":"fresh","fresh":true},"checks":[{"name":"daemon health","ok":true,"evidence":"health ok"}]}`))
	}))
	defer ts.Close()
	host, port := splitTestURL(t, ts.URL)
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)

	var out bytes.Buffer
	cmd := newProbeCmd()
	cmd.SetArgs([]string{"data", "--json", "--readiness", "--require-secret-backend", "native", "--fail"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if sawAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q", sawAuth)
	}
	if strings.Contains(rawQuery, token) || !strings.Contains(rawQuery, "readiness=1") || !strings.Contains(rawQuery, "requireSecretBackend=native") {
		t.Fatalf("query = %q", rawQuery)
	}
	text := out.String()
	if !strings.Contains(text, `"ok": true`) || !strings.Contains(text, `"checkedAccounts": 2`) {
		t.Fatalf("output = %s", text)
	}
	if strings.Contains(text, token) {
		t.Fatalf("output leaked token: %s", text)
	}
}

func TestProbeDataCmdFailReportsNotReady(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	token := "tok-probe-fail"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusFailedDependency)
		w.Write([]byte(`{"ok":false,"health":{"version":"test","protocolVersion":"0.1","secretBackend":"file"},"accountsCheck":{"provider":"codex","secretBackend":"file","checkedAccounts":1},"checks":[{"name":"multi-account readiness","ok":false,"evidence":"checked 1, need 2","nextStep":"import another account"}],"errors":[{"source":"accounts/check","code":-32602,"message":"expected multiple Codex accounts, found 1"}]}`))
	}))
	defer ts.Close()
	host, port := splitTestURL(t, ts.URL)
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)

	var out bytes.Buffer
	cmd := newProbeCmd()
	cmd.SetArgs([]string{"data", "--json", "--readiness", "--fail"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "probe data failed") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), "multi-account readiness") {
		t.Fatalf("output = %s", out.String())
	}
	if strings.Contains(out.String(), token) {
		t.Fatalf("output leaked token: %s", out.String())
	}
}

func TestProbeDataCmdRejectsUnauthorized(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeTokenForTest(home, "tok-probe-unauthorized"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"ok":false,"error":"unauthorized"}`))
	}))
	defer ts.Close()
	host, port := splitTestURL(t, ts.URL)
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)

	cmd := newProbeCmd()
	cmd.SetArgs([]string{"data"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("err = %v", err)
	}
}
