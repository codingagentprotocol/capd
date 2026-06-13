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

func TestServiceConfigPersistsNetworkOptions(t *testing.T) {
	cfg := serviceConfig("/bin/capd", serviceOptions{
		Host:    "127.0.0.2",
		Port:    8888,
		Origins: []string{"http://localhost:3000", "https://app.example.test"},
	})
	if got := strings.Join(cfg.Arguments, " "); got != "start --host 127.0.0.2 --port 8888 --origins http://localhost:3000 --origins https://app.example.test" {
		t.Fatalf("arguments = %q", got)
	}
}

func TestServiceInstallOptionsUsesSecretBackendEnv(t *testing.T) {
	t.Setenv("CAPD_SECRET_BACKEND", " native ")
	cmd := newServiceCmd()
	install, _, err := cmd.Find([]string{"install"})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := serviceOptionsFor("install", install, "", "", 0, nil)
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
	opts, err := serviceOptionsFor("install", install, "file", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.SecretBackend != "file" {
		t.Fatalf("secret backend = %q, want file", opts.SecretBackend)
	}
}

func TestServiceInstallOptionsPersistsNetworkEnv(t *testing.T) {
	t.Setenv("CAPD_HOST", "127.0.0.2")
	t.Setenv("CAPD_PORT", "8888")
	t.Setenv("CAPD_ORIGINS", "http://localhost:3000, https://app.example.test ")
	cmd := newServiceCmd()
	install, _, err := cmd.Find([]string{"install"})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := serviceOptionsFor("install", install, "", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Host != "127.0.0.2" || opts.Port != 8888 || strings.Join(opts.Origins, ",") != "http://localhost:3000,https://app.example.test" {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestServiceInstallOptionsFlagsOverrideNetworkEnv(t *testing.T) {
	t.Setenv("CAPD_HOST", "127.0.0.2")
	t.Setenv("CAPD_PORT", "8888")
	t.Setenv("CAPD_ORIGINS", "http://localhost:3000")
	cmd := newServiceCmd()
	cmd.SetArgs([]string{"install", "--host", "127.0.0.3", "--port", "9999", "--origins", "https://app.example.test"})
	install, _, err := cmd.Find([]string{"install", "--host", "127.0.0.3", "--port", "9999", "--origins", "https://app.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := install.ParseFlags([]string{"--host", "127.0.0.3", "--port", "9999", "--origins", "https://app.example.test"}); err != nil {
		t.Fatal(err)
	}
	opts, err := serviceOptionsFor("install", install, "", "127.0.0.3", 9999, []string{"https://app.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Host != "127.0.0.3" || opts.Port != 9999 || strings.Join(opts.Origins, ",") != "https://app.example.test" {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestServiceInstallOptionsRejectsUnknownSecretBackendEnv(t *testing.T) {
	t.Setenv("CAPD_SECRET_BACKEND", "mystery")
	cmd := newServiceCmd()
	install, _, err := cmd.Find([]string{"install"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = serviceOptionsFor("install", install, "", "", 0, nil)
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
	for _, want := range []string{"--secret-backend", "file", "native", "CAPD_SECRET_BACKEND", "--host", "CAPD_HOST", "--port", "CAPD_PORT", "--origins", "CAPD_ORIGINS"} {
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
