package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestServiceConfigDefaultsToPlainStart(t *testing.T) {
	cfg := serviceConfig("/bin/capd", serviceOptions{})
	if got := strings.Join(cfg.Arguments, " "); got != "start" {
		t.Fatalf("arguments = %q, want start", got)
	}
}

func TestServiceConfigPersistsSecretBackend(t *testing.T) {
	cfg := serviceConfig("/bin/capd", serviceOptions{SecretBackend: "native"})
	if got := strings.Join(cfg.Arguments, " "); got != "start --secret-backend native" {
		t.Fatalf("arguments = %q, want start --secret-backend native", got)
	}
}

func TestServiceInstallHelpDocumentsSecretBackend(t *testing.T) {
	var out bytes.Buffer
	cmd := newServiceCmd()
	cmd.SetArgs([]string{"install", "--help"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"--secret-backend", "file", "native", "CAPD_SECRET_BACKEND"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help missing %q: %s", want, text)
		}
	}
}

func TestServiceInstallRejectsUnknownSecretBackendFlag(t *testing.T) {
	cmd := newServiceCmd()
	cmd.SetArgs([]string{"install", "--secret-backend", "mystery"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `unknown secret backend "mystery"`) {
		t.Fatalf("err = %v", err)
	}
}
