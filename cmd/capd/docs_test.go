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
		"fresh-quota error includes safe `data.accountRoute` and\n`data.routeCandidates` evidence",
		"failing text output includes the selected route and route-candidate quota evidence",
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

func TestReferenceDocsCoverSecretMigrationReadbackSafety(t *testing.T) {
	data, err := os.ReadFile("../../docs/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	reference := string(data)

	for _, want := range []string{
		"`capd accounts codex migrate-secrets [account-id\\|all]",
		"The target secret is read back before account metadata is updated",
		"if target readback fails, capd removes the attempted target secret",
		"keeps the source ref",
		"reports safe partial evidence",
		"add `--delete-source` only after native readiness passes",
	} {
		if !strings.Contains(reference, want) {
			t.Fatalf("reference docs missing secret migration readback contract %q", want)
		}
	}
}

func TestReferenceDocsCoverRunFreshQuotaRecovery(t *testing.T) {
	data, err := os.ReadFile("../../docs/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	reference := string(data)

	for _, want := range []string{
		"`--account auto --require-fresh-quota` fails",
		"`capd accounts check --readiness`",
		"`capd agents route --account auto --require-fresh-quota`",
		"prints any safe daemon-provided\n`accountRoute` and `routeCandidates` evidence",
		"preview the route\ngate before sending another prompt",
	} {
		if !strings.Contains(reference, want) {
			t.Fatalf("reference docs missing run fresh quota recovery contract %q", want)
		}
	}
}

func TestReferenceDocsCoverBrowserTokenCleanup(t *testing.T) {
	data, err := os.ReadFile("../../docs/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	reference := string(data)

	for _, want := range []string{
		"`?token=TOKEN` remains supported",
		"remove `token` from the visible URL",
		"`history.replaceState`",
		"`capd.auth.*`\nsubprotocol",
		"do not persist daemon tokens in localStorage or sessionStorage",
	} {
		if !strings.Contains(reference, want) {
			t.Fatalf("reference docs missing browser token cleanup contract %q", want)
		}
	}
}

func TestTestingDocsCoverLinuxSecretStoreStdinSafety(t *testing.T) {
	data, err := os.ReadFile("../../docs/testing.md")
	if err != nil {
		t.Fatal(err)
	}
	testingDoc := string(data)

	for _, want := range []string{
		"Linux native storage requires `secret-tool`",
		"token bundles go through stdin",
		"failing `secret-tool store`\ncommands omit command output",
		"cannot\nleak access or refresh tokens into capd errors",
	} {
		if !strings.Contains(testingDoc, want) {
			t.Fatalf("testing docs missing Linux SecretStore stdin safety contract %q", want)
		}
	}
}
