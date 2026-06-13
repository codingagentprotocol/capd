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
		`"secretBackend": "native"`,
		"safe `secretBackend` enum",
		"safe\nhuman-readable `reason`",
		"current-account\ntie-break",
		"`accountRoute` should match the first",
		"route candidate\ncount",
		"`accountRoute`, `routeCandidates`, and `secretBackend` evidence",
		"prefer that safe account\nSecretStore backend when present",
		"`data.routeCandidates`, `data.routePolicy`, and `data.secretBackend` evidence",
		"`routePolicy` is safe to display",
		"without exposing token\nmaterial or SecretStore refs",
		`{"provider", "currentAccountId", "secretBackend", "checkedAccounts", "quotaRefreshed", "summary", "autoRoute", "routeCandidates", "accounts"}`,
		"`routeCandidates` is included with the",
		"same ordering, `reason`, safe `secretBackend`, and redaction contract",
		"why `autoRoute` was selected without\nmaking a second route",
		"partial evidence may still include cached\n`routeCandidates`",
		"fresh-quota error includes safe `data.accountRoute`,\n`data.routeCandidates`, `data.routePolicy`, and `data.secretBackend` evidence",
		"failing text output includes the selected route, route policy, and route-candidate quota evidence",
		`fresh-quota failures also print ` + "`{\"ok\":false,\"error\", \"data\", \"nextSteps\"}`",
	} {
		if !strings.Contains(reference, want) {
			t.Fatalf("reference docs missing route candidate contract %q", want)
		}
	}
}

func TestDocsCoverPromptFreeBrowserProbeRefresh(t *testing.T) {
	readmeData, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	referenceData, err := os.ReadFile("../../docs/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	testingData, err := os.ReadFile("../../docs/testing.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(readmeData)
	reference := string(referenceData)
	testingDoc := string(testingData)

	for _, want := range []string{
		"full\nconsole's ordinary diagnostic refresh",
		"ordinary `Refresh` path\nuse `accounts/list` metadata plus route evidence",
		"opening either page does\nnot read account SecretStore credentials",
		"not checked in prompt-free refresh",
		"ordinary next\nsteps point first to `capd doctor --prompt-free`",
		"diagnostic package viewer",
		"`manifest.json` or `summary.json`",
		"without reading local files or exposing daemon tokens",
		"compact QA report for route policy, route candidates, quota freshness",
		"capd probe evidence --manifest /tmp/capd-live-evidence/manifest.json --fail",
	} {
		if !strings.Contains(readme, want) {
			t.Fatalf("README missing prompt-free web refresh contract %q", want)
		}
	}
	for _, forbidden := range []string{
		"Ordinary probe\nrefreshes follow the daemon's active SecretStore backend",
		"opening either page reads account SecretStore credentials",
	} {
		if strings.Contains(readme, forbidden) {
			t.Fatalf("README contains stale prompt-prone web refresh wording %q", forbidden)
		}
	}

	for _, want := range []string{
		"full console's ordinary diagnostic refresh",
		"ordinary `Refresh` path use `accounts/list` metadata",
		"opening either page does not read account SecretStore credentials",
		"console's\n`就绪门禁` action",
		"probe's `Readiness` button run the stronger account",
		"`not checked in prompt-free refresh`",
		"ordinary Console and Probe next steps prefer `capd doctor --prompt-free`",
	} {
		if !strings.Contains(reference, want) {
			t.Fatalf("reference docs missing prompt-free probe refresh contract %q", want)
		}
	}
	for _, want := range []string{
		"full console's ordinary diagnostic refresh",
		"`Refresh` path use `accounts/list` metadata plus route evidence",
		"do not read\naccount SecretStore credentials",
		"console's `就绪门禁`",
		"without repeated OS credential prompts",
		"ordinary next steps\nprefer `capd doctor --prompt-free`",
		"diagnostic package viewer",
		"`manifest.json` or `summary.json`",
		"without reading local\nfiles from the browser",
		"compact QA report\nfor route policy, route candidates, quota freshness",
		"capd probe evidence --manifest\n/tmp/capd-live-evidence/manifest.json --fail",
	} {
		if !strings.Contains(testingDoc, want) {
			t.Fatalf("testing docs missing prompt-free probe refresh contract %q", want)
		}
	}
}

func TestReferenceDocsCoverProbeEvidence(t *testing.T) {
	data, err := os.ReadFile("../../docs/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	reference := string(data)
	for _, want := range []string{
		"capd probe evidence --manifest <manifest.json\\|summary.json>",
		"validates saved live selftest evidence without contacting the daemon",
		"follows artifact paths from the manifest or summary",
		"route policy, route candidate count, fresh quota evidence",
		"emits a `checks` array for CI/Web consumers",
		"`--fail` exits non-zero",
	} {
		if !strings.Contains(reference, want) {
			t.Fatalf("reference docs missing probe evidence contract %q", want)
		}
	}
}

func TestDocsCoverDaemonImportAuthPathListEnv(t *testing.T) {
	readmeData, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	referenceData, err := os.ReadFile("../../docs/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	testingData, err := os.ReadFile("../../docs/testing.md")
	if err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]string{
		"README":    string(readmeData),
		"reference": string(referenceData),
		"testing":   string(testingData),
	} {
		for _, want := range []string{
			"CAPD_CODEX_AUTH_PATHS",
			"capd accounts import",
		} {
			if !strings.Contains(doc, want) {
				t.Fatalf("%s missing daemon import env contract %q", name, want)
			}
		}
	}
	reference := string(referenceData)
	for _, want := range []string{
		"OS path-list of auth files",
		"`:` on macOS/Linux, `;` on Windows",
		"matching the CAP/WebSocket path used by web clients",
	} {
		if !strings.Contains(reference, want) {
			t.Fatalf("reference docs missing daemon import env detail %q", want)
		}
	}
}

func TestReferenceDocsCoverAccountListRouteAudit(t *testing.T) {
	data, err := os.ReadFile("../../docs/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	reference := string(data)

	for _, want := range []string{
		"`capd accounts list [--json]`",
		"`capd accounts codex list [--json]`",
		"`quotaFresh`",
		"`routeScore`",
		"`routeReason`",
		"without reading SecretStore token material",
		"without SecretStore refs or token material",
	} {
		if !strings.Contains(reference, want) {
			t.Fatalf("reference docs missing account list route audit contract %q", want)
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
		"`CAPD_SECRET_BACKEND=native capd accounts check --json --readiness --require-secret-backend native --timeout 2m`",
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
		"`capd accounts check --json --readiness`",
		"`LIVE_SECRET_BACKEND=<backend> make live-codex-preflight`",
		"`capd agents route --account auto --require-fresh-quota --json`",
		"prints any safe daemon-provided\n`accountRoute`, `routeCandidates`, and `secretBackend` evidence",
		"prefer that safe account\nSecretStore backend when present",
		"preview the\nroute gate before sending another prompt",
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

func TestTestingDocsCoverLiveSelftestDaemonSafety(t *testing.T) {
	data, err := os.ReadFile("../../docs/testing.md")
	if err != nil {
		t.Fatal(err)
	}
	testingDoc := string(data)

	for _, want := range []string{
		"make live-codex-selftest",
		`LIVE_RUN_PROMPT=1 LIVE_PROMPT="say ready" make live-codex-selftest`,
		"should not depend on a second terminal",
		"reuses an already healthy\ndaemon",
		"starts a\ntemporary foreground daemon in the background",
		"cleans up that temporary process on exit",
		"If the live preflight fails",
		"prints a prompt-free readiness gap\nsummary",
		"daemon health, safe `capd accounts codex list --json` metadata",
		"multi-account smoke gate",
		"by default it does not run SecretStore-reading checks",
		"`LIVE_DIAGNOSE_SECRETSTORE=1`",
		"SecretStore-reading checks",
		"`capd doctor --json --fail --verify-secretstore`",
		"`capd accounts check --json\n--readiness`",
		"authenticated `/probe/data` readiness",
		"reports a different\nSecretStore backend",
		"fails immediately",
		"instead of trying to start a second process on the same port",
		"`CAPD_LIVE_EVIDENCE_DIR=/tmp/capd-live-evidence`",
		"`manifest.json` index",
		"primary evidence paths",
		"validate\nthat package with `capd probe evidence --manifest ... --fail`",
	} {
		if !strings.Contains(testingDoc, want) {
			t.Fatalf("testing docs missing live selftest daemon safety contract %q", want)
		}
	}
}

func TestTestingDocsCoverPreflightPromptAvoidance(t *testing.T) {
	data, err := os.ReadFile("../../docs/testing.md")
	if err != nil {
		t.Fatal(err)
	}
	testingDoc := string(data)

	for _, want := range []string{
		"runs the\nmulti-account smoke gate before native SecretStore roundtrip prompts",
		"missing second account fails fast without unnecessary OS approval dialogs",
	} {
		if !strings.Contains(testingDoc, want) {
			t.Fatalf("testing docs missing preflight prompt avoidance contract %q", want)
		}
	}
}
