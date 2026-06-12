package main

import (
	"os"
	"strings"
	"testing"
)

func TestReferenceDocsCoverRouteCandidateEvidence(t *testing.T) {
	data, err := os.ReadFile("../../docs/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	reference := string(data)

	for _, want := range []string{
		`"routeCandidates": [{"accountId": "codex-acct"`,
		"`routeCandidates` contains the same safe evidence for every",
		"`accountRoute` should match the first",
		"route candidate\ncount",
		`{"provider", "currentAccountId", "secretBackend", "checkedAccounts", "quotaRefreshed", "summary", "autoRoute", "routeCandidates", "accounts"}`,
		"`routeCandidates` is included with the",
		"why `autoRoute` was selected without making a second route call",
		"partial evidence may still include cached\n`routeCandidates`",
	} {
		if !strings.Contains(reference, want) {
			t.Fatalf("reference docs missing route candidate contract %q", want)
		}
	}
}

func TestReferenceDocsCoverProbeReadinessBackendDefaults(t *testing.T) {
	data, err := os.ReadFile("../../docs/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	reference := string(data)

	for _, want := range []string{
		"`--readiness` requests the stronger readiness view and defaults the daemon request to `requireSecretBackend=native`",
		"use `--require-secret-backend file` only for intentional file-backend tests",
		"`?readiness=1` defaults to `requireSecretBackend=native`",
		"`?readiness=1&requireSecretBackend=file` is reserved for intentional file-backend tests",
		"unknown values fail fast with HTTP 400 before quota or route checks run",
	} {
		if !strings.Contains(reference, want) {
			t.Fatalf("reference docs missing probe readiness backend default contract %q", want)
		}
	}
}
