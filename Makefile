VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LIVE_PROMPT ?= say ready
LIVE_SECRET_BACKEND ?= native
LDFLAGS := -X github.com/codingagentprotocol/capd/internal/daemon.Version=$(VERSION)

.PHONY: build run test vet tidy verify verify-secretstore verify-codex-readiness-sim live-codex-preflight live-codex-readiness

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
	go test ./internal/account -run 'Test(QuotaFromUsageRedactsSensitiveRawJSON|QuotaFromUsageNormalizesOutOfRangePercentsConservatively|QuotaSnapshotFreshRejectsInvalidPrimaryPercent|SelectQuotaRouteAccountTreatsInvalidQuotaAsUnknown)$$' -count=1
	go test ./internal/server -run 'Test(AgentsRouteAutoAccountChoosesLowestCachedQuota|AgentsRouteAutoAccountRequireFreshQuota|AgentsRouteAutoAccountIgnoresStaleLowQuota|SessionCreateAutoAccountBindsLowestCachedQuota|AccountsCheckCanRefreshQuotaAndEnforceReadiness|AccountsCheckReadinessFailureIsDaemonSideAndSafe|AccountsCheckAllFreshFailureReportsEveryStaleAccountSafely|AccountsQuotaAllRefreshesEveryCodexAccountSafely|ProbeDataReturnsSafeAccountRouteEvidence|ProbeDataReadinessReturnsPartialEvidenceOnFailure|ProbeDataReadinessDefaultsToNativeAndAvoidsQuotaOnBackendMismatch|ProbeServedWithSecurityHeaders|ProbeValidationRowsStayUnique|ConsoleStaticContract|ConsoleApprovalRendererHasSingleBoxDeclaration)$$' -count=1
	go test ./cmd/capd -run 'Test(AccountsCheckReadinessShortcutSetsDaemonGateParams|DoctorReportsMultiAccountQuotaAndAutoRoute|DoctorChecksDaemonAccountsThroughCAP|CodexAccountsSmokeRequireAllFreshQuota|CodexAccountsSmokeTextIncludesAutoRouteEvidence|RouteCLIAccountAutoRequireFreshQuotaFailsWhenMissing|RouteCLIAccountAutoRequireFreshQuotaPassesWithFreshCache|CodexAccountsQuotaAllRefreshesEveryAccountSafely|ProbeDataTextPrintsReadinessSummary|ProbeDataTextPrintsPartialRouteCandidates|ProbeDataReadinessCanOverrideRequiredSecretBackend|HealthRequireSecretBackendFailsOnMismatch|SecretStoreCheckJSONRoundTrip|MigrateCodexAccountSecretsVerifiesTargetReadableBeforeMetadataUpdate|RunTaskFreshQuotaFailureSuggestsReadiness|ReferenceDocsCoverRunFreshQuotaRecovery|ReferenceDocsCoverBrowserTokenCleanup)$$' -count=1
	go test ./internal/adapter/codex -run 'TestProbeUsesResolvedCodexBinary$$' -count=1

live-codex-preflight:
	@echo "live-codex-preflight requires >=2 imported Codex accounts, CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND), and a running daemon from: capd start --secret-backend $(LIVE_SECRET_BACKEND)"
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd secretstore check --json --roundtrip --require-backend $(LIVE_SECRET_BACKEND) --timeout 2m
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd accounts --secret-backend $(LIVE_SECRET_BACKEND) codex smoke --json --require-multiple --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd health --json --require-secret-backend $(LIVE_SECRET_BACKEND)
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd accounts --secret-backend $(LIVE_SECRET_BACKEND) codex quota all --timeout 2m
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd accounts --secret-backend $(LIVE_SECRET_BACKEND) codex smoke --json --quota --require-multiple --require-fresh-quota --require-all-fresh-quota --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd doctor --json --fail --verify-secretstore --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m
	@echo "checking daemon CAP/WebSocket readiness"
	@probe_url=$$(CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd console --probe --url --require-secret-backend $(LIVE_SECRET_BACKEND)); curl -fsS "$$probe_url" >/dev/null
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd accounts check --json --timeout 2m
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd accounts check --json --readiness --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd probe data --json --readiness --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m --fail
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd agents usage codex --account auto
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd agents route --account auto --require-fresh-quota --json

live-codex-readiness: live-codex-preflight
	@echo "running live Codex prompt with quota-aware auto account"
	CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd run --agent codex --account auto --require-fresh-quota "$(LIVE_PROMPT)"
