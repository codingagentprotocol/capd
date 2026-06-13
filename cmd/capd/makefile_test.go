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
		"verify-codex-readiness-sim live-codex-repair-plan live-codex-repair-commands live-codex-preflight live-codex-readiness live-codex-selftest",
		"live-codex-repair-plan:",
		"@CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) doctor --repair-plan --prompt-free --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m",
		"live-codex-repair-commands:",
		"@CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) doctor --repair-commands --prompt-free --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m",
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
		"ProbeEvidenceCmdSummarizesManifestArtifacts",
		"ProbeEvidenceCmdJSONAndFail",
		"ProbeEvidenceCmdWritesStandaloneHTMLReport",
		"HealthRequireSecretBackendFailsOnMismatch",
		"SecretStoreCheckJSONRoundTrip",
		"SecretStoreCheckFailsOnBackendMismatchAfterJSON",
		"MigrateCodexAccountSecretsVerifiesTargetReadableBeforeMetadataUpdate",
		"RunTaskFreshQuotaFailureSuggestsReadiness",
		"RunTaskFreshQuotaFailurePrefersRouteSecretBackend",
		"ReferenceDocsCoverRouteCandidateEvidence",
		"ReferenceDocsCoverProbeEvidence",
		"DocsCoverPromptFreeBrowserProbeRefresh",
		"ReferenceDocsCoverRunFreshQuotaRecovery",
		"ReferenceDocsCoverBrowserTokenCleanup",
		"go test ./internal/adapter/adaptertest ./internal/adapter/codex ./internal/adapter/claudecode ./internal/adapter/gemini ./internal/adapter/opencode ./internal/adapter/cursoragent",
		"ConformanceHelpers",
		".*AdapterConformanceStaticContract",
		"ProbeUsesResolvedCodexBinary",
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
		`repair_plan="${CAPD_LIVE_REPAIR_PLAN:-}"`,
		`evidence_dir="${CAPD_LIVE_EVIDENCE_DIR:-}"`,
		`evidence_manifest=""`,
		`evidence_report=""`,
		`evidence_health=""`,
		`evidence_accounts=""`,
		`evidence_route=""`,
		`evidence_probe=""`,
		`evidence_doctor=""`,
		`evidence_smoke=""`,
		`evidence_accounts_check=""`,
		`evidence_manifest="$evidence_dir/manifest.json"`,
		`evidence_report="$evidence_dir/report.html"`,
		"bin_owned=0",
		`daemon_mode="existing"`,
		"evidence_manifest_path()",
		`prefix="${evidence_dir%/}/"`,
		`rel="${path#"$prefix"}"`,
		"write_summary()",
		`if [ -z "$summary" ]; then`,
		`date -u '+%Y-%m-%dT%H:%M:%SZ'`,
		`"summaryVersion": 1`,
		`"checkedAt": "%s"`,
		`"daemonMode": "%s"`,
		`"logPath": "%s"`,
		`"bin": "%s"`,
		`"repairPlanPath": "%s"`,
		`"evidenceDir": "%s"`,
		`"evidenceManifestPath": "%s"`,
		`"evidenceReportPath": "%s"`,
		`"routeEvidencePath": "%s"`,
		`"probeEvidencePath": "%s"`,
		`"doctorEvidencePath": "%s"`,
		`>"$summary"`,
		`warning: failed to write live summary`,
		"write_evidence_manifest()",
		`route_json="$(json_escape "$(evidence_manifest_path "$evidence_route")")"`,
		`probe_json="$(json_escape "$(evidence_manifest_path "$evidence_probe")")"`,
		`doctor_json="$(json_escape "$(evidence_manifest_path "$evidence_doctor")")"`,
		`report_json="$(json_escape "$(evidence_manifest_path "$evidence_report")")"`,
		`"manifestVersion": 1`,
		`"artifacts": {`,
		`"accountsList": "%s"`,
		`"agentsRoute": "%s"`,
		`"probeData": "%s"`,
		`"accountsCheck": "%s"`,
		`"report": "%s"`,
		`} >"$evidence_manifest"`,
		"prepare_evidence_dir()",
		`mkdir -p "$evidence_dir"`,
		"capture_evidence()",
		`path="$evidence_dir/$name"`,
		`if "$@" >"$path"; then`,
		"write_repair_plan()",
		`if [ -z "$repair_plan" ]; then`,
		`"$bin" doctor --prompt-free --json --fail --require-secret-backend "$backend" --timeout 2m >"$repair_plan" 2>/dev/null`,
		`warning: failed to write live repair plan`,
		"write_success_evidence()",
		`evidence_route="$evidence_dir/agents-route.json"`,
		`evidence_probe="$evidence_dir/probe-data-readiness.json"`,
		`evidence_doctor="$evidence_dir/doctor-prompt-free.json"`,
		`evidence_manifest="$evidence_dir/manifest.json"`,
		`evidence_report="$evidence_dir/report.html"`,
		`prepare_evidence_dir || return $?`,
		`"$bin" agents route --account auto --require-fresh-quota --json >"$evidence_route" || return $?`,
		`"$bin" probe data --json --readiness --require-secret-backend "$backend" --timeout 2m --fail >"$evidence_probe" || return $?`,
		`"$bin" doctor --prompt-free --json --fail --require-secret-backend "$backend" --timeout 2m >"$evidence_doctor" || return $?`,
		"verify_success_evidence()",
		`"$bin" probe evidence --manifest "$evidence_manifest" --html "$evidence_report" --fail`,
		"write_failure_evidence_report()",
		`"$bin" probe evidence --manifest "$evidence_manifest" --html "$evidence_report"`,
		`write_summary "running" "initializing"`,
		`go build -o "$bin" ./cmd/capd`,
		`write_summary "running" "daemon-health"`,
		`"$bin" health --json --require-secret-backend "$backend"`,
		`daemon_mode="existing"`,
		"health_any_backend()",
		"but not with ${backend} SecretStore",
		`"$bin" health --json >&2 || true`,
		"restart it with: capd start --secret-backend $backend",
		`write_repair_plan`,
		`write_summary "failed" "secret-backend"`,
		`daemon_mode="temporary"`,
		`"$bin" start --host "$host" --port "$port" --secret-backend "$backend"`,
		`write_summary "failed" "daemon-health"`,
		`write_summary "failed" "daemon-start"`,
		`kill "$daemon_pid"`,
		`if [ "$bin_owned" -eq 1 ]; then`,
		`rm -f "$bin"`,
		"live-codex-preflight failed; safe diagnostics follow",
		`write_repair_plan`,
		`write_summary "running" "live-codex-preflight"`,
		`write_summary "failed" "live-codex-preflight"`,
		`evidence_health="$evidence_dir/health.json"`,
		`evidence_accounts="$evidence_dir/accounts-list.json"`,
		`evidence_smoke="$evidence_dir/accounts-smoke.json"`,
		`evidence_report="$evidence_dir/report.html"`,
		`capture_evidence "agents-route.json" "$bin" agents route --account auto --require-fresh-quota --json || true`,
		`capture_evidence "probe-data-prompt-free.json" "$bin" probe data --json --timeout 2m || true`,
		`write_evidence_manifest "failed" "live-codex-preflight"`,
		`write_failure_evidence_report || true`,
		"readiness gaps to resolve: >=2 imported Codex accounts, fresh quota for auto-route/all accounts, ${backend} SecretStore, and daemon/Web readiness",
		`diagnose_secretstore="${LIVE_DIAGNOSE_SECRETSTORE:-0}"`,
		`capture_evidence "health.json" "$bin" health --json --require-secret-backend "$backend" || true`,
		`capture_evidence "accounts-list.json" "$bin" accounts --secret-backend "$backend" codex list --json || true`,
		`capture_evidence "accounts-smoke.json" "$bin" accounts --secret-backend "$backend" codex smoke --json --require-multiple --require-secret-backend "$backend" --timeout 2m || true`,
		`case "$diagnose_secretstore" in`,
		`capture_evidence "doctor-secretstore.json" "$bin" doctor --json --fail --verify-secretstore --require-secret-backend "$backend" --timeout 2m || true`,
		`capture_evidence "accounts-check-readiness.json" "$bin" accounts check --json --readiness --require-secret-backend "$backend" --timeout 2m || true`,
		`capture_evidence "probe-data-readiness.json" "$bin" probe data --json --readiness --require-secret-backend "$backend" --timeout 2m --fail || true`,
		`LIVE_RUN_PROMPT`,
		`if ! make live-codex-preflight LIVE_SECRET_BACKEND="$backend" CAPD_BIN="$bin"; then`,
		`"$bin" run --agent codex --account auto --require-fresh-quota "$prompt"`,
		`write_summary "running" "live-prompt"`,
		`write_summary "failed" "live-prompt"`,
		`write_summary "running" "evidence"`,
		`if ! write_success_evidence; then`,
		`write_summary "failed" "evidence"`,
		`if ! write_evidence_manifest "passed" "complete"`,
		`failed to write live Codex evidence manifest`,
		`if ! verify_success_evidence; then`,
		`failed to validate live Codex evidence manifest`,
		`write_summary "passed" "complete"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("live selftest script missing safety contract %q", want)
		}
	}
	if strings.Contains(script, `if ! make live-codex-preflight LIVE_SECRET_BACKEND="$backend"; then`) {
		t.Fatal("live selftest must pass CAPD_BIN so preflight reuses the tested binary")
	}
	promptGate := strings.Index(script, `case "$run_prompt" in`)
	successEvidence := strings.LastIndex(script, `if ! write_success_evidence; then`)
	finalPassed := strings.LastIndex(script, `write_summary "passed" "complete"`)
	if !(promptGate < successEvidence && successEvidence < finalPassed) {
		t.Fatal("live selftest must write success evidence before final passed summary")
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
	if !(strings.Index(defaultFailureBlock, `capture_evidence "accounts-list.json" "$bin" accounts --secret-backend "$backend" codex list --json || true`) < strings.Index(defaultFailureBlock, `capture_evidence "agents-route.json" "$bin" agents route --account auto --require-fresh-quota --json || true`) &&
		strings.Index(defaultFailureBlock, `capture_evidence "agents-route.json" "$bin" agents route --account auto --require-fresh-quota --json || true`) < strings.Index(defaultFailureBlock, `capture_evidence "probe-data-prompt-free.json" "$bin" probe data --json --timeout 2m || true`) &&
		strings.Index(defaultFailureBlock, `capture_evidence "probe-data-prompt-free.json" "$bin" probe data --json --timeout 2m || true`) < strings.Index(defaultFailureBlock, `capture_evidence "accounts-smoke.json" "$bin" accounts --secret-backend "$backend" codex smoke --json --require-multiple --require-secret-backend "$backend" --timeout 2m || true`)) {
		t.Fatal("live selftest default failure diagnostics must show route evidence and prompt-free Web probe data before smoke")
	}
	for _, forbidden := range []string{`capture_evidence "doctor-secretstore.json"`, `capture_evidence "accounts-check-readiness.json"`, `capture_evidence "probe-data-readiness.json"`} {
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
