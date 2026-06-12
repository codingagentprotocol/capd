package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codingagentprotocol/capd/internal/config"
)

func TestHealthCmdChecksDaemonHealthz(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("format") == "json" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"ok":true,"daemon":"capd","version":"test-version","protocolVersion":"0.1","secretBackend":"native"}`))
			return
		}
		w.Write([]byte("ok\n"))
	}))
	defer ts.Close()
	host, port := splitTestURL(t, ts.URL)
	t.Setenv("CAPD_HOST", host)
	t.Setenv("CAPD_PORT", port)

	var out bytes.Buffer
	cmd := newHealthCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "ok" {
		t.Fatalf("out = %q", out.String())
	}

	out.Reset()
	cmd = newHealthCmd()
	cmd.SetArgs([]string{"--json"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got struct {
		OK              bool   `json:"ok"`
		Addr            string `json:"addr"`
		Daemon          string `json:"daemon"`
		Version         string `json:"version"`
		ProtocolVersion string `json:"protocolVersion"`
		SecretBackend   string `json:"secretBackend"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Addr != net.JoinHostPort(host, port) || got.Daemon != "capd" || got.Version != "test-version" || got.ProtocolVersion != "0.1" || got.SecretBackend != "native" {
		t.Fatalf("json = %+v", got)
	}
}

func TestHealthJSONFallsBackForOldDaemon(t *testing.T) {
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
	cmd := newHealthCmd()
	cmd.SetArgs([]string{"--json"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got struct {
		OK   bool   `json:"ok"`
		Addr string `json:"addr"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Addr != net.JoinHostPort(host, port) {
		t.Fatalf("json = %+v", got)
	}
}

func TestDaemonHealthFailureSuggestsStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := daemonHealth(ctx, config.Config{Host: "127.0.0.1", Port: 1})
	if err == nil || !strings.Contains(err.Error(), "capd start") || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("err = %v", err)
	}
}

func splitTestURL(t *testing.T, raw string) (string, string) {
	t.Helper()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(raw, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}
