package main

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codingagentprotocol/capd/internal/config"
)

func TestDaemonWSURLEncodesToken(t *testing.T) {
	raw := daemonWSURL(config.Config{Host: "127.0.0.1", Port: 7777}, "tok &with?chars")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "ws" || u.Host != "127.0.0.1:7777" || u.Path != "/ws" {
		t.Fatalf("url = %q", raw)
	}
	if got := u.Query().Get("token"); got != "tok &with?chars" {
		t.Fatalf("token = %q", got)
	}
	if u.RawQuery == "token=tok &with?chars" {
		t.Fatalf("token was not escaped: %q", raw)
	}
}

func TestDaemonWSURLHandlesIPv6Host(t *testing.T) {
	raw := daemonWSURL(config.Config{Host: "::1", Port: 7777}, "tok")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "[::1]:7777" || u.Query().Get("token") != "tok" {
		t.Fatalf("url = %q", raw)
	}
}

func TestConsoleURLEncodesToken(t *testing.T) {
	raw := consoleURL(config.Config{Host: "localhost", Port: 17777}, "tok+with&chars")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "http" || u.Host != "localhost:17777" || u.Path != "/console/" {
		t.Fatalf("url = %q", raw)
	}
	if got := u.Query().Get("token"); got != "tok+with&chars" {
		t.Fatalf("token = %q", got)
	}
}

func TestDaemonAddrOmitsToken(t *testing.T) {
	cfg := config.Config{Host: "127.0.0.1", Port: 7777}
	if got := daemonAddr(cfg); got != "127.0.0.1:7777" {
		t.Fatalf("addr = %q", got)
	}
	if strings.Contains(daemonAddr(cfg), "token") {
		t.Fatalf("display address contains token material")
	}
}

func TestRunTaskConnectErrorDoesNotLeakToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", "1")
	token := "tok &with?chars"
	if err := writeTokenForTest(home, token); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := newRunCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	err := runTask(cmd, runOpts{agent: "codex", prompt: "hello"})
	if err == nil {
		t.Fatal("expected connect error")
	}
	text := err.Error()
	if strings.Contains(text, token) || strings.Contains(text, url.QueryEscape(token)) {
		t.Fatalf("connect error leaked token: %s", text)
	}
	if !strings.Contains(text, "127.0.0.1:1") {
		t.Fatalf("connect error missing display address: %s", text)
	}
}

func writeTokenForTest(home, token string) error {
	dir := filepath.Join(home, ".capd")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "token"), []byte(token+"\n"), 0o600)
}
