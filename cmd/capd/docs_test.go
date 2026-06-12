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
		`{"provider", "currentAccountId", "secretBackend", "checkedAccounts", "quotaRefreshed", "autoRoute", "routeCandidates", "accounts"}`,
		"`routeCandidates` is included with the",
		"why `autoRoute` was selected without making a second route call",
		"partial evidence may still include cached\n`routeCandidates`",
	} {
		if !strings.Contains(reference, want) {
			t.Fatalf("reference docs missing route candidate contract %q", want)
		}
	}
}
