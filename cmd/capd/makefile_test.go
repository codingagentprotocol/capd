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
		"verify-codex-readiness-sim live-codex-preflight live-codex-readiness",
		"live-codex-preflight:",
		"live-codex-readiness: live-codex-preflight",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd secretstore check --json --roundtrip --require-backend $(LIVE_SECRET_BACKEND) --timeout 2m",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd doctor --json --fail --verify-secretstore --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd accounts --secret-backend $(LIVE_SECRET_BACKEND) codex smoke --json --require-multiple --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd health --json --require-secret-backend $(LIVE_SECRET_BACKEND)",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd accounts --secret-backend $(LIVE_SECRET_BACKEND) codex quota all --timeout 2m",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd accounts --secret-backend $(LIVE_SECRET_BACKEND) codex smoke --json --quota --require-multiple --require-fresh-quota --require-all-fresh-quota --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd accounts check --json --timeout 2m",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd accounts check --json --readiness --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd probe data --json --readiness --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m --fail",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd agents usage codex --account auto",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd agents route --account auto --require-fresh-quota",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd run --agent codex --account auto --require-fresh-quota",
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
	} {
		if strings.Contains(makefile, forbidden) {
			t.Fatalf("Makefile contains backend-drift-prone command %q", forbidden)
		}
	}
	quotaAll := "CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd accounts --secret-backend $(LIVE_SECRET_BACKEND) codex quota all --timeout 2m"
	freshSmoke := "CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd accounts --secret-backend $(LIVE_SECRET_BACKEND) codex smoke --json --quota --require-multiple --require-fresh-quota --require-all-fresh-quota --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m"
	doctor := "CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd doctor --json --fail --verify-secretstore --require-secret-backend $(LIVE_SECRET_BACKEND) --timeout 2m"
	if !(strings.Index(makefile, quotaAll) < strings.Index(makefile, freshSmoke) && strings.Index(makefile, freshSmoke) < strings.Index(makefile, doctor)) {
		t.Fatal("live-codex-preflight must refresh quota and run fresh smoke before doctor --fail")
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
		"AgentsRouteAutoAccountChoosesLowestCachedQuota",
		"AgentsRouteAutoAccountRequireFreshQuota",
		"AgentsRouteAutoAccountIgnoresStaleLowQuota",
		"SessionCreateAutoAccountBindsLowestCachedQuota",
		"AccountsCheckCanRefreshQuotaAndEnforceReadiness",
		"AccountsCheckReadinessFailureIsDaemonSideAndSafe",
		"AccountsCheckAllFreshFailureReportsEveryStaleAccountSafely",
		"AccountsQuotaAllRefreshesEveryCodexAccountSafely",
		"AccountsCheckReadinessShortcutSetsDaemonGateParams",
		"DoctorReportsMultiAccountQuotaAndAutoRoute",
		"DoctorChecksDaemonAccountsThroughCAP",
		"CodexAccountsSmokeRequireAllFreshQuota",
		"CodexAccountsSmokeTextIncludesAutoRouteEvidence",
		"RouteCLIAccountAutoRequireFreshQuotaPassesWithFreshCache",
		"CodexAccountsQuotaAllRefreshesEveryAccountSafely",
	} {
		if !strings.Contains(makefile, want) {
			t.Fatalf("Makefile simulated readiness target missing %q", want)
		}
	}
}
