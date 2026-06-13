package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/codingagentprotocol/capd/internal/audit"
)

func TestSecretStoreCheckJSONRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd := newSecretStoreCmd()
	cmd.SetArgs([]string{"check", "--json", "--roundtrip", "--require-backend", "file"})
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got secretStoreReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Backend != "file" || got.RoundTrip == nil || !got.RoundTrip.OK {
		t.Fatalf("report = %+v", got)
	}
	events, err := audit.Recent("", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "secretstore.check" || events[0].Outcome != "ok" || events[0].Data["backend"] != "file" || events[0].Data["roundTrip"] != true {
		t.Fatalf("audit events = %+v", events)
	}
	for _, leaked := range []string{home, "doctor-secretstore-check", "capd-doctor"} {
		if strings.Contains(out.String(), leaked) {
			t.Fatalf("secretstore check leaked %q: %s", leaked, out.String())
		}
	}
}

func TestSecretStoreCheckFailsOnBackendMismatchAfterJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd := newSecretStoreCmd()
	cmd.SetArgs([]string{"check", "--json", "--require-backend", "native"})
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "secretstore check failed") {
		t.Fatalf("err = %v", err)
	}
	var got secretStoreReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK || got.Backend != "file" || !containsString(got.Issues, `secret backend is "file", want "native"`) {
		t.Fatalf("report = %+v", got)
	}
	want := "restart or rerun with: capd secretstore check --secret-backend native --require-backend native --timeout 2m"
	if !containsString(got.NextSteps, want) {
		t.Fatalf("nextSteps = %+v", got.NextSteps)
	}
	if len(got.Checks) == 0 || got.Checks[0].NextStep != want {
		t.Fatalf("checks = %+v", got.Checks)
	}
}

func TestSecretStoreCheckHelpIncludesTimeout(t *testing.T) {
	var out bytes.Buffer
	cmd := newSecretStoreCmd()
	cmd.SetArgs([]string{"check", "--help"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "--timeout") || !strings.Contains(text, "2m") {
		t.Fatalf("help missing timeout: %s", text)
	}
}

func TestSecretStoreCheckRejectsUnknownBackend(t *testing.T) {
	cmd := newSecretStoreCmd()
	cmd.SetArgs([]string{"check", "--secret-backend", "mystery"})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `unknown secret backend "mystery"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestSecretStoreRoundTripNextStepsArePlatformSpecificAndSafe(t *testing.T) {
	tests := []struct {
		name     string
		backend  string
		err      error
		contains []string
	}{
		{
			name:    "mac keychain denied",
			backend: "native",
			err:     errors.New("macOS keychain status -128"),
			contains: []string{
				"approve macOS Keychain access",
				"capd secretstore check --secret-backend native --roundtrip --require-backend native --timeout 2m",
				"file SecretStore",
			},
		},
		{
			name:    "linux secret service",
			backend: "native",
			err:     errors.New("secret-tool store failed: Secret Service is locked"),
			contains: []string{
				"install libsecret secret-tool",
				"unlock the Linux Secret Service/keyring",
				"capd secretstore check --secret-backend native --roundtrip --require-backend native --timeout 2m",
			},
		},
		{
			name:    "windows credential manager",
			backend: "native",
			err:     errors.New("Credential Manager unavailable"),
			contains: []string{
				"Windows Credential Manager",
				"capd secretstore check --secret-backend native --roundtrip --require-backend native --timeout 2m",
			},
		},
		{
			name:    "file backend",
			backend: "file",
			err:     errors.New("write failed"),
			contains: []string{
				"rerun SecretStore roundtrip",
				"capd secretstore check --secret-backend file --roundtrip --require-backend file --timeout 2m",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := secretStoreRoundTripNextStep(tc.backend, tc.err)
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Fatalf("next step %q missing %q", got, want)
				}
			}
			for _, leaked := range []string{"access_token", "refresh_token", "native-access-secret", "native-refresh-secret"} {
				if strings.Contains(got, leaked) {
					t.Fatalf("next step leaked %q: %s", leaked, got)
				}
			}
		})
	}
}
