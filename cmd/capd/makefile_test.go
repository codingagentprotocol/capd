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
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd doctor --json --fail --require-secret-backend $(LIVE_SECRET_BACKEND)",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd accounts --secret-backend $(LIVE_SECRET_BACKEND) codex smoke --require-multiple --require-secret-backend $(LIVE_SECRET_BACKEND)",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd accounts --secret-backend $(LIVE_SECRET_BACKEND) codex quota all",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd accounts --secret-backend $(LIVE_SECRET_BACKEND) codex smoke --quota --require-multiple --require-fresh-quota --require-all-fresh-quota --require-secret-backend $(LIVE_SECRET_BACKEND)",
		"go run ./cmd/capd accounts check --refresh-quota --require-multiple --require-fresh-quota --require-all-fresh-quota --require-secret-backend $(LIVE_SECRET_BACKEND)",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd agents usage codex --account auto",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd agents route --account auto --require-fresh-quota",
		"CAPD_SECRET_BACKEND=$(LIVE_SECRET_BACKEND) go run ./cmd/capd run --agent codex --account auto --require-fresh-quota",
	} {
		if !strings.Contains(makefile, want) {
			t.Fatalf("Makefile missing live readiness contract %q", want)
		}
	}
	for _, forbidden := range []string{
		"go run ./cmd/capd accounts codex smoke",
		"go run ./cmd/capd accounts codex quota all",
		"go run ./cmd/capd doctor --json --fail --require-secret-backend native",
	} {
		if strings.Contains(makefile, forbidden) {
			t.Fatalf("Makefile contains backend-drift-prone command %q", forbidden)
		}
	}
}
