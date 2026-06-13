package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codingagentprotocol/capd/internal/audit"
)

func TestSupportBundleWritesSafeEvidencePackage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeTokenForTest(home, "tok-support-secret"); err != nil {
		t.Fatal(err)
	}
	if err := audit.Append("", audit.Event{
		Time:    123,
		Type:    "agents.route",
		Actor:   "cli",
		Outcome: "ok",
		Data: map[string]any{
			"agent":     "codex",
			"account":   "codex-low",
			"token":     "tok-support-secret",
			"secretRef": "file://hidden",
			"authJSON":  `{"access_token":"hidden"}`,
			"localPath": filepath.Join(home, ".capd", "auth.json"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", "1")
	outDir := filepath.Join(t.TempDir(), "bundle")

	var out bytes.Buffer
	cmd := newSupportCmd()
	cmd.SetArgs([]string{"bundle", "--out", outDir, "--probe-data=false", "--timeout", "2s"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"bundle: " + outDir, "manifest: " + filepath.Join(outDir, "manifest.json"), "report: " + filepath.Join(outDir, "report.html"), "ok: false"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q: %s", want, text)
		}
	}
	for _, path := range []string{"manifest.json", "doctor-prompt-free.json", "agents-route.json", "audit.json", "health.json", "report.html"} {
		if _, err := os.Stat(filepath.Join(outDir, path)); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["status"] != "failed" || manifest["stage"] != "support-bundle" || manifest["daemonMode"] != "external" {
		t.Fatalf("manifest = %+v", manifest)
	}
	for _, name := range []string{"manifest.json", "doctor-prompt-free.json", "agents-route.json", "audit.json", "health.json", "report.html"} {
		data, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, leaked := range []string{"tok-support-secret", "file://hidden", "access_token", home} {
			if strings.Contains(string(data), leaked) {
				t.Fatalf("%s leaked %q", name, leaked)
			}
		}
	}
	auditData, err := os.ReadFile(filepath.Join(outDir, "audit.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(auditData), `"type": "agents.route"`) || !strings.Contains(string(auditData), `"agent": "codex"`) {
		t.Fatalf("audit artifact missing safe event metadata: %s", auditData)
	}
	for _, unsafe := range []string{"secretRef", "authJSON", "localPath", "token"} {
		if strings.Contains(string(auditData), unsafe) {
			t.Fatalf("audit artifact kept unsafe key %q: %s", unsafe, auditData)
		}
	}
}

func TestSupportBundleFailUsesEvidenceReport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", "1")

	var out bytes.Buffer
	cmd := newSupportCmd()
	cmd.SetArgs([]string{"bundle", "--out", filepath.Join(t.TempDir(), "bundle"), "--probe-data=false", "--timeout", "2s", "--fail"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "support bundle evidence failed") {
		t.Fatalf("err = %v output=%s", err, out.String())
	}
	if !strings.Contains(out.String(), "issue:") {
		t.Fatalf("output missing evidence issues: %s", out.String())
	}
}
