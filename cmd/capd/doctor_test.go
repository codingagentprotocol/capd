package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDoctorJSONReportsMissingReadinessWithoutSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", "1")
	if err := writeTokenForTest(home, "tok-doctor-secret"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := newDoctorCmd()
	cmd.SetArgs([]string{"--json"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got doctorReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK {
		t.Fatalf("doctor unexpectedly ok: %+v", got)
	}
	if got.Daemon.OK || got.Codex.ImportedAccounts != 0 {
		t.Fatalf("report = %+v", got)
	}
	body := out.String()
	for _, leaked := range []string{"tok-doctor-secret", home} {
		if strings.Contains(body, leaked) {
			t.Fatalf("doctor JSON leaked %q: %s", leaked, body)
		}
	}
	for _, want := range []string{"daemon health check failed", "no imported Codex accounts", "multi-account readiness requires at least two imported Codex accounts"} {
		if !strings.Contains(body, want) {
			t.Fatalf("doctor JSON missing %q: %s", want, body)
		}
	}
}

func TestDoctorTextReturnsErrorWhenNotReady(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", "1")
	var out bytes.Buffer
	cmd := newDoctorCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "readiness issue") {
		t.Fatalf("err = %v", err)
	}
	text := out.String()
	for _, want := range []string{"capd doctor: needs attention", "daemon:", "codex:", "issues:", "next steps:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor text missing %q: %s", want, text)
		}
	}
}
