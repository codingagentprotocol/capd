VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LIVE_PROMPT ?= say ready
LIVE_SECRET_BACKEND ?= native
CAPD_BIN ?= go run ./cmd/capd
LDFLAGS := -X github.com/codingagentprotocol/capd/internal/daemon.Version=$(VERSION)

.PHONY: build run test vet tidy verify verify-secretstore verify-codex-readiness-sim live-codex-preflight live-codex-readiness live-codex-selftest

build:
	go build -ldflags "$(LDFLAGS)" -o capd ./cmd/capd

run: build
	./capd start

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

verify:
	go test ./...
	go vet ./...
	go build ./...
	go test -race ./internal/server ./internal/account/... ./cmd/capd

verify-secretstore:
	CAPD_TEST_NATIVE_SECRET=1 go test ./internal/account/secret -run TestNativeStoreRoundTrip -count=1
	GOOS=linux GOARCH=amd64 go test -c ./internal/account/secret -o /tmp/capd-secret-linux.test
	GOOS=windows GOARCH=amd64 go test -c ./internal/account/secret -o /tmp/capd-secret-windows.test.exe
	CGO_ENABLED=0 go test ./internal/account/secret

verify-codex-readiness-sim:
	@echo "running deterministic simulated Codex multi-account quota/routing/readiness gates"
	sh -n scripts/live_codex_selftest.sh
	go test ./internal/account -run 'Test(QuotaFromUsageRedactsSensitiveRawJSON|QuotaFromUsageNormalizesOutOfRangePercentsConservatively|QuotaSnapshotFreshRejectsInvalidPrimaryPercent|SelectQuotaRouteAccountTreatsInvalidQuotaAsUnknown|ConcurrentQuotaRefreshAndRouting|QuotaRouteEvidenceAndReason)$$' -count=1
	go test ./internal/server -run 'Test(AgentsRouteAutoAccountChoosesLowestCachedQuota|AgentsRouteAutoAccountRequireFreshQuota|AgentsRouteAutoAccountIgnoresStaleLowQuota|SessionCreateAutoAccountBindsLowestCachedQuota|ClientDisconnectDoesNotEndLiveSessionAndReconnectCanContinue|AccountsCheckCanRefreshQuotaAndEnforceReadiness|AccountsCheckReadinessFailureIsDaemonSideAndSafe|AccountsCheckRefreshFailureKeepsSuccessfulQuotaEvidence|AccountsCheckAllFreshFailureReportsEveryStaleAccountSafely|AccountsQuotaAllRefreshesEveryCodexAccountSafely|AccountsQuotaAllFailureIsSafeAndCachesSuccessfulAccounts|ConcurrentAccountsQuotaAllAndFreshRoute|ProbeDataReturnsSafeAccountRouteEvidence|ProbeDataReadinessReturnsPartialEvidenceOnFailure|ProbeDataReadinessDefaultsToNativeAndAvoidsQuotaOnBackendMismatch|ProbeDataNextStepsUseRunnableNativeSecretStoreRetry|ProbeCredentialNextStepUsesSecretState|ProbeServedWithSecurityHeaders|ProbeValidationRowsStayUnique|ConsoleStaticContract|ConsoleApprovalRendererHasSingleBoxDeclaration)$$' -count=1
	go test ./cmd/capd -run 'Test(AccountsImportNextStepPreservesEnvBackend|AccountsCheckReadinessShortcutSetsDaemonGateParams|AccountsCheckErrorNextStepsExplainSecretAccessDenied|AccountsCheckErrorNextStepsPreserveRequiredSecretBackend|AccountsCheckRefreshQuotaFailureDoesNotLeakSecrets|DoctorReportsMultiAccountQuotaAndAutoRoute|DoctorChecksDaemonAccountsThroughCAP|DoctorDaemonAccountsCheckUsesCallerTimeout|DoctorQuotaNextStepsPreferRouteSecretBackend|DoctorQuotaNextStepsHonorExplicitRequiredSecretBackend|DoctorSecretReadinessNextStepUsesSecretState|CodexAccountsSmokeRequireAllFreshQuota|CodexAccountsSmokeBackendMismatchNextSteps|CodexAccountsSmokeQuotaNextStepsPreserveSecretBackend|CodexAccountsSmokeRequireMultipleReturnsPartialAccountEvidence|CodexAccountsSmokeSecretNextStepsPreserveSecretBackend|CodexAccountsSmokeTextIncludesAutoRouteEvidence|CodexSmokeCachedAccountRowReportsSecretBackendMetadata|RouteCLIAccountAutoRequireFreshQuotaFailsWhenMissing|RouteCLIAccountAutoRequireFreshQuotaPreservesEnvBackendInNextStep|RouteCLIAccountAutoRequireFreshQuotaPrefersAccountSecretBackendInNextStep|RouteCLIAccountAutoRequireFreshQuotaPassesWithFreshCache|CodexAccountsQuotaAllRefreshesEveryAccountSafely|CodexAccountsQuotaAllFailurePrintsSafePartialEvidence|ProbeDataTextPrintsReadinessSummary|ProbeDataTextPrintsPartialRouteCandidates|ProbeDataReadinessCanOverrideRequiredSecretBackend|HealthRequireSecretBackendFailsOnMismatch|SecretStoreCheckJSONRoundTrip|SecretStoreCheckFailsOnBackendMismatchAfterJSON|MigrateCodexAccountSecretsVerifiesTargetReadableBeforeMetadataUpdate|RunTaskFreshQuotaFailureSuggestsReadiness|RunTaskFreshQuotaFailurePrefersRouteSecretBackend|ReferenceDocsCoverRouteCandidateEvidence|DocsCoverPromptFreeBrowserProbeRefresh|ReferenceDocsCoverRunFreshQuotaRecovery|ReferenceDocsCoverBrowserTokenCleanup)$$' -count=1
	go test ./internal/adapter/codex -run 'TestProbeUsesResolvedCodexBinary$$' -count=1

live-codex-preflight:
	@echo "live-codex-preflight requires >=2 imported Codex accounts, CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND), and a running daemon from: capd start --secret-backend $(LIVE_SECRET_BACKEND)"
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) accounts --secret-backend $(LIVE_SECRET_BACKEND) codex list --json
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) accounts --secret-backend $(LIVE_SECRET_BACKEND) codex smoke --json --require-multiple --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) secretstore check --json --roundtrip --require-backend $(LIVE_SECRET_BACKEND) --timeout 2m
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) health --json --require-secret-backend $(LIVE_SECRET_BACKEND)
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) accounts --secret-backend $(LIVE_SECRET_BACKEND) codex quota all --timeout 2m
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) accounts --secret-backend $(LIVE_SECRET_BACKEND) codex smoke --json --quota --require-multiple --require-fresh-quota --require-all-fresh-quota --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) doctor --json --fail --verify-secretstore --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m
	@echo "checking daemon CAP/WebSocket readiness"
	@probe_url=$$(CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) console --probe --url --require-secret-backend $(LIVE_SECRET_BACKEND)); curl -fsS "$$probe_url" >/dev/null
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) accounts check --json --timeout 2m
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) accounts check --json --readiness --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) probe data --json --readiness --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m --fail
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) agents usage codex --account auto
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) agents route --account auto --require-fresh-quota --json

live-codex-readiness: live-codex-preflight
	@echo "running live Codex prompt with quota-aware auto account"
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) $(CAPD_BIN) run --agent codex --account auto --require-fresh-quota "$(LIVE_PROMPT)"

live-codex-selftest:
	./scripts/live_codex_selftest.sh
