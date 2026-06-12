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

func TestServiceInstallOptionsUsesSecretBackendEnv(t *testing.T) {
	t.Setenv("CAPD_SECRET_BACKEND", " native ")
	cmd := newServiceCmd()
	install, _, err := cmd.Find([]string{"install"})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := serviceOptionsFor("install", install, "")
	if err != nil {
		t.Fatal(err)
	}
	if opts.SecretBackend != "native" {
		t.Fatalf("secret backend = %q, want native", opts.SecretBackend)
	}
}

func TestServiceInstallOptionsFlagOverridesSecretBackendEnv(t *testing.T) {
	t.Setenv("CAPD_SECRET_BACKEND", "native")
	cmd := newServiceCmd()
	cmd.SetArgs([]string{"install", "--secret-backend", "file"})
	install, _, err := cmd.Find([]string{"install", "--secret-backend", "file"})
	if err != nil {
		t.Fatal(err)
	}
	if err := install.ParseFlags([]string{"--secret-backend", "file"}); err != nil {
		t.Fatal(err)
	}
	opts, err := serviceOptionsFor("install", install, "file")
	if err != nil {
		t.Fatal(err)
	}
	if opts.SecretBackend != "file" {
		t.Fatalf("secret backend = %q, want file", opts.SecretBackend)
	}
}

func TestServiceInstallOptionsRejectsUnknownSecretBackendEnv(t *testing.T) {
	t.Setenv("CAPD_SECRET_BACKEND", "mystery")
	cmd := newServiceCmd()
	install, _, err := cmd.Find([]string{"install"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = serviceOptionsFor("install", install, "")
	if err == nil || !strings.Contains(err.Error(), `unknown secret backend "mystery"`) {
		t.Fatalf("err = %v", err)
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
