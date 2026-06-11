package main

import (
	"net/url"
	"testing"

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
