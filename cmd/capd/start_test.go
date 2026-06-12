package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestStartHelpDocumentsSecretBackend(t *testing.T) {
	var out bytes.Buffer
	cmd := newStartCmd()
	cmd.SetArgs([]string{"--help"})
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

func TestStartRejectsUnknownSecretBackendFlag(t *testing.T) {
	cmd := newStartCmd()
	cmd.SetArgs([]string{"--secret-backend", "mystery"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `unknown secret backend "mystery"`) {
		t.Fatalf("err = %v", err)
	}
}
