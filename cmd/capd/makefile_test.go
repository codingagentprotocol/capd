package main

import (
	"os"
	"strings"
	"testing"
)

func TestLiveCodexReadinessUsesOneSecretBackend(t *testing.T) {
	data, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(data)
	for _, want := range []string{
		"LIVE_SECRET_BACKEND ?= native",
		"CAPD_BIN ?= go run ./cmd/capd",
		"verify-codex-readiness-sim live-codex-preflight live-codex-readiness live-codex-selftest",
		"live-codex-preflight:",
		"running daemon from: capd start --secret-backend $(LIVE_SECRET_BACKEND)",
		"live-codex-readiness: live-codex-preflight",
		"live-codex-selftest:",
		"./scripts/live_codex_selftest.sh",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) secretstore check --json --roundtrip --require-backend $(LIVE_SECRET_BACKEND) --timeout 2m",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) accounts --secret-backend $(LIVE_SECRET_BACKEND) codex list --json",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) doctor --json --fail --verify-secretstore --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) accounts --secret-backend $(LIVE_SECRET_BACKEND) codex smoke --json --require-multiple --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) health --json --require-secret-backend $(LIVE_SECRET_BACKEND)",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) accounts --secret-backend $(LIVE_SECRET_BACKEND) codex quota all --timeout 2m",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) accounts --secret-backend $(LIVE_SECRET_BACKEND) codex smoke --json --quota --require-multiple --require-fresh-quota --require-all-fresh-quota --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m",
		"probe_url=$$(CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) console --probe --url --require-secret-backend $(LIVE_SECRET_BACKEND)); curl -fsS \"$$probe_url\" >/dev/null",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) accounts check --json --timeout 2m",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) accounts check --json --readiness --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) probe data --json --readiness --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m --fail",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) agents usage codex --account auto",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) agents route --account auto --require-fresh-quota --json",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) run --agent codex --account auto --require-fresh-quota",
	} {
		if !strings.Contains(makefile, want) {
			t.Fatalf("Makefile missing live readiness contract %q", want)
		}
	}
	for _, forbidden := range []string{
		"\n\tgo run ./cmd/capd accounts codex smoke",
		"\n\tgo run ./cmd/capd accounts codex quota all",
		"\n\tgo run ./cmd/capd accounts check --json",
		"\n\tgo run ./cmd/capd doctor --json --fail --require-secret-backend native",
		"running daemon from: CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) capd start",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd agents route --account auto --require-fresh-quota\n",
	} {
		if strings.Contains(makefile, forbidden) {
			t.Fatalf("Makefile contains backend-drift-prone command %q", forbidden)
		}
	}
	quotaAll := "CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) accounts --secret-backend $(LIVE_SECRET_BACKEND) codex quota all --timeout 2m"
	listAudit := "CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) accounts --secret-backend $(LIVE_SECRET_BACKEND) codex list --json"
	initialSmoke := "CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) accounts --secret-backend $(LIVE_SECRET_BACKEND) codex smoke --json --require-multiple --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m"
	secretRoundTrip := "CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) secretstore check --json --roundtrip --require-backend $(LIVE_SECRET_BACKEND) --timeout 2m"
	freshSmoke := "CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) accounts --secret-backend $(LIVE_SECRET_BACKEND) codex smoke --json --quota --require-multiple --require-fresh-quota --require-all-fresh-quota --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m"
	doctor := "CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) doctor --json --fail --verify-secretstore --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m"
	consoleProbe := "probe_url=$$(CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) console --probe --url --require-secret-backend $(LIVE_SECRET_BACKEND)); curl -fsS \"$$probe_url\" >/dev/null"
	probeData := "CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) probe data --json --readiness --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m --fail"
	if !(strings.Index(makefile, listAudit) < strings.Index(makefile, initialSmoke) && strings.Index(makefile, initialSmoke) < strings.Index(makefile, secretRoundTrip)) {
		t.Fatal("live-codex-preflight must check imported account count before native SecretStore roundtrip prompts")
	}
	if !(strings.Index(makefile, quotaAll) < strings.Index(makefile, freshSmoke) && strings.Index(makefile, freshSmoke) < strings.Index(makefile, doctor)) {
		t.Fatal("live-codex-preflight must refresh quota and run fresh smoke before doctor --fail")
	}
	if !(strings.Index(makefile, listAudit) < strings.Index(makefile, freshSmoke)) {
		t.Fatal("live-codex-preflight must print account route audit before fresh smoke")
	}
	if !(strings.Index(makefile, doctor) < strings.Index(makefile, consoleProbe) && strings.Index(makefile, consoleProbe) < strings.Index(makefile, probeData)) {
		t.Fatal("live-codex-preflight must validate the tokenized probe URL before HTTP probe data")
	}
}

func TestSimulatedCodexReadinessTargetCoversCoreGates(t *testing.T) {
	data, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(data)
	for _, want := range []string{
		"verify-codex-readiness-sim:",
		"running deterministic simulated Codex multi-account quota/routing/readiness gates",
		"sh -n scripts/live_codex_selftest.sh",
		"AccountsCheckErrorNextStepsPreserveRequiredSecretBackend",
		"QuotaFromUsageRedactsSensitiveRawJSON",
		"QuotaFromUsageNormalizesOutOfRangePercentsConservatively",
		"QuotaSnapshotFreshRejectsInvalidPrimaryPercent",
		"SelectQuotaRouteAccountTreatsInvalidQuotaAsUnknown",
		"ConcurrentQuotaRefreshAndRouting",
		"QuotaRouteEvidenceAndReason",
		"AgentsRouteAutoAccountChoosesLowestCachedQuota",
		"AgentsRouteAutoAccountRequireFreshQuota",
		"AgentsRouteAutoAccountIgnoresStaleLowQuota",
		"SessionCreateAutoAccountBindsLowestCachedQuota",
		"AccountsCheckCanRefreshQuotaAndEnforceReadiness",
		"AccountsCheckReadinessFailureIsDaemonSideAndSafe",
		"AccountsCheckRefreshFailureKeepsSuccessfulQuotaEvidence",
		"AccountsCheckAllFreshFailureReportsEveryStaleAccountSafely",
		"AccountsQuotaAllRefreshesEveryCodexAccountSafely",
		"AccountsQuotaAllFailureIsSafeAndCachesSuccessfulAccounts",
		"ConcurrentAccountsQuotaAllAndFreshRoute",
		"ProbeDataReturnsSafeAccountRouteEvidence",
		"ProbeDataReadinessReturnsPartialEvidenceOnFailure",
		"ProbeDataReadinessDefaultsToNativeAndAvoidsQuotaOnBackendMismatch",
		"ProbeDataNextStepsUseRunnableNativeSecretStoreRetry",
		"ProbeCredentialNextStepUsesSecretState",
		"ProbeServedWithSecurityHeaders",
		"ProbeValidationRowsStayUnique",
		"ConsoleStaticContract",
		"ConsoleApprovalRendererHasSingleBoxDeclaration",
		"AccountsImportNextStepPreservesEnvBackend",
		"AccountsCheckReadinessShortcutSetsDaemonGateParams",
		"AccountsCheckErrorNextStepsExplainSecretAccessDenied",
		"AccountsCheckErrorNextStepsPreserveRequiredSecretBackend",
		"AccountsCheckRefreshQuotaFailureDoesNotLeakSecrets",
		"DoctorReportsMultiAccountQuotaAndAutoRoute",
		"DoctorChecksDaemonAccountsThroughCAP",
		"DoctorDaemonAccountsCheckUsesCallerTimeout",
		"DoctorQuotaNextStepsPreferRouteSecretBackend",
		"DoctorQuotaNextStepsHonorExplicitRequiredSecretBackend",
		"DoctorSecretReadinessNextStepUsesSecretState",
		"CodexAccountsSmokeRequireAllFreshQuota",
		"CodexAccountsSmokeBackendMismatchNextSteps",
		"CodexAccountsSmokeQuotaNextStepsPreserveSecretBackend",
		"CodexAccountsSmokeRequireMultipleReturnsPartialAccountEvidence",
		"CodexAccountsSmokeSecretNextStepsPreserveSecretBackend",
		"CodexAccountsSmokeTextIncludesAutoRouteEvidence",
		"CodexSmokeCachedAccountRowReportsSecretBackendMetadata",
		"RouteCLIAccountAutoRequireFreshQuotaFailsWhenMissing",
		"RouteCLIAccountAutoRequireFreshQuotaPreservesEnvBackendInNextStep",
		"RouteCLIAccountAutoRequireFreshQuotaPrefersAccountSecretBackendInNextStep",
		"RouteCLIAccountAutoRequireFreshQuotaPassesWithFreshCache",
		"CodexAccountsQuotaAllRefreshesEveryAccountSafely",
		"CodexAccountsQuotaAllFailurePrintsSafePartialEvidence",
		"ProbeDataTextPrintsReadinessSummary",
		"ProbeDataReadinessCanOverrideRequiredSecretBackend",
		"HealthRequireSecretBackendFailsOnMismatch",
		"SecretStoreCheckJSONRoundTrip",
		"SecretStoreCheckFailsOnBackendMismatchAfterJSON",
		"MigrateCodexAccountSecretsVerifiesTargetReadableBeforeMetadataUpdate",
		"RunTaskFreshQuotaFailureSuggestsReadiness",
		"RunTaskFreshQuotaFailurePrefersRouteSecretBackend",
		"ReferenceDocsCoverRouteCandidateEvidence",
		"DocsCoverPromptFreeBrowserProbeRefresh",
		"ReferenceDocsCoverRunFreshQuotaRecovery",
		"ReferenceDocsCoverBrowserTokenCleanup",
		"go test ./internal/adapter/codex -run 'TestProbeUsesResolvedCodexBinary$$' -count=1",
	} {
		if !strings.Contains(makefile, want) {
			t.Fatalf("Makefile simulated readiness target missing %q", want)
		}
	}
}

func TestLiveCodexSelftestScriptHandlesTemporaryDaemonSafely(t *testing.T) {
	data, err := os.ReadFile("../../scripts/live_codex_selftest.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, want := range []string{
		`bin="${CAPD_LIVE_DAEMON_BIN:-${TMPDIR:-/tmp}/capd-live-daemon-$$}"`,
		`summary="${CAPD_LIVE_SUMMARY:-}"`,
		"bin_owned=0",
		`daemon_mode="existing"`,
		"write_summary()",
		`if [ -z "$summary" ]; then`,
		`date -u '+%Y-%m-%dT%H:%M:%SZ'`,
		`"summaryVersion": 1`,
		`"checkedAt": "%s"`,
		`"daemonMode": "%s"`,
		`"logPath": "%s"`,
		`"bin": "%s"`,
		`>"$summary"`,
		`warning: failed to write live summary`,
		`write_summary "running" "initializing"`,
		`go build -o "$bin" ./cmd/capd`,
		`write_summary "running" "daemon-health"`,
		`"$bin" health --json --require-secret-backend "$backend"`,
		`daemon_mode="existing"`,
		"health_any_backend()",
		"but not with ${backend} SecretStore",
		`"$bin" health --json >&2 || true`,
		"restart it with: capd start --secret-backend $backend",
		`write_summary "failed" "secret-backend"`,
		`daemon_mode="temporary"`,
		`"$bin" start --host "$host" --port "$port" --secret-backend "$backend"`,
		`write_summary "failed" "daemon-health"`,
		`write_summary "failed" "daemon-start"`,
		`kill "$daemon_pid"`,
		`if [ "$bin_owned" -eq 1 ]; then`,
		`rm -f "$bin"`,
		"live-codex-preflight failed; safe diagnostics follow",
		`write_summary "running" "live-codex-preflight"`,
		`write_summary "failed" "live-codex-preflight"`,
		"readiness gaps to resolve: >=2 imported Codex accounts, fresh quota for auto-route/all accounts, ${backend} SecretStore, and daemon/Web readiness",
		`diagnose_secretstore="${LIVE_DIAGNOSE_SECRETSTORE:-0}"`,
		`"$bin" health --json --require-secret-backend "$backend" || true`,
		`"$bin" accounts --secret-backend "$backend" codex list --json || true`,
		`"$bin" agents route --account auto --require-fresh-quota --json || true`,
		`"$bin" probe data --json --timeout 2m || true`,
		`"$bin" accounts --secret-backend "$backend" codex smoke --json --require-multiple --require-secret-backend "$backend" --timeout 2m || true`,
		`case "$diagnose_secretstore" in`,
		`"$bin" doctor --json --fail --verify-secretstore --require-secret-backend "$backend" --timeout 2m || true`,
		`"$bin" accounts check --json --readiness --require-secret-backend "$backend" --timeout 2m || true`,
		`"$bin" probe data --json --readiness --require-secret-backend "$backend" --timeout 2m --fail || true`,
		`LIVE_RUN_PROMPT`,
		`if ! make live-codex-preflight LIVE_SECRET_BACKEND="$backend" CAPD_BIN="$bin"; then`,
		`"$bin" run --agent codex --account auto --require-fresh-quota "$prompt"`,
		`write_summary "running" "live-prompt"`,
		`write_summary "failed" "live-prompt"`,
		`write_summary "passed" "complete"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("live selftest script missing safety contract %q", want)
		}
	}
	if strings.Contains(script, `if ! make live-codex-preflight LIVE_SECRET_BACKEND="$backend"; then`) {
		t.Fatal("live selftest must pass CAPD_BIN so preflight reuses the tested binary")
	}
	optionalDoctor := `"$bin" doctor --json --fail --verify-secretstore --require-secret-backend "$backend" --timeout 2m || true`
	optionalAccountsCheck := `"$bin" accounts check --json --readiness --require-secret-backend "$backend" --timeout 2m || true`
	optionalProbe := `"$bin" probe data --json --readiness --require-secret-backend "$backend" --timeout 2m --fail || true`
	gate := strings.Index(script, `case "$diagnose_secretstore" in`)
	if strings.Index(script, optionalDoctor) < gate || strings.Index(script, optionalAccountsCheck) < gate || strings.Index(script, optionalProbe) < gate {
		t.Fatal("live selftest must keep prompt-prone diagnostics behind optional SecretStore gate")
	}
	failureStart := strings.Index(script, "live-codex-preflight failed; safe diagnostics follow")
	if failureStart < 0 || gate < failureStart {
		t.Fatal("live selftest failure block must exist before optional SecretStore gate")
	}
	defaultFailureBlock := script[failureStart:gate]
	if !(strings.Index(defaultFailureBlock, `"$bin" accounts --secret-backend "$backend" codex list --json || true`) < strings.Index(defaultFailureBlock, `"$bin" agents route --account auto --require-fresh-quota --json || true`) &&
		strings.Index(defaultFailureBlock, `"$bin" agents route --account auto --require-fresh-quota --json || true`) < strings.Index(defaultFailureBlock, `"$bin" probe data --json --timeout 2m || true`) &&
		strings.Index(defaultFailureBlock, `"$bin" probe data --json --timeout 2m || true`) < strings.Index(defaultFailureBlock, `"$bin" accounts --secret-backend "$backend" codex smoke --json --require-multiple --require-secret-backend "$backend" --timeout 2m || true`)) {
		t.Fatal("live selftest default failure diagnostics must show route evidence and prompt-free Web probe data before smoke")
	}
	for _, forbidden := range []string{`"$bin" doctor `, `"$bin" accounts check --json --readiness`, `"$bin" probe data --json --readiness`} {
		if strings.Contains(defaultFailureBlock, forbidden) {
			t.Fatalf("live selftest default failure diagnostics must stay prompt-free, found %q", forbidden)
		}
	}
}

func TestVerifySecretStoreTargetCoversNativeBackends(t *testing.T) {
	data, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(data)
	for _, want := range []string{
		"verify-secretstore:",
		"CAPD_TEST_NATIVE_SECRET=1 go test ./internal/account/secret -run TestNativeStoreRoundTrip -count=1",
		"GOOS=linux GOARCH=amd64 go test -c ./internal/account/secret -o /tmp/capd-secret-linux.test",
		"GOOS=windows GOARCH=amd64 go test -c ./internal/account/secret -o /tmp/capd-secret-windows.test.exe",
		"CGO_ENABLED=0 go test ./internal/account/secret",
	} {
		if !strings.Contains(makefile, want) {
			t.Fatalf("Makefile verify-secretstore target missing %q", want)
		}
	}
}
