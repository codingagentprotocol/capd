package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestRouteCLIPrefersRequestedCapabilities(t *testing.T) {
	infos := []protocol.AgentInfo{
		{ID: "opencode", Available: true, Capabilities: protocol.AgentCapabilities{Streaming: true}},
		{ID: "codex", Available: true, Capabilities: protocol.AgentCapabilities{Streaming: true, Review: true}},
	}
	result, err := routeCLI(infos, nil, routeCLIParams{
		Capabilities: protocol.AgentCapabilities{Review: true},
		Prefer:       []string{"opencode", "codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Agent.ID != "codex" || !strings.Contains(result.Reason, "review") {
		t.Fatalf("result = %+v", result)
	}
}

func TestRouteCLITrimsPreference(t *testing.T) {
	infos := []protocol.AgentInfo{
		{ID: "codex", Available: true, Capabilities: protocol.AgentCapabilities{Streaming: true}},
		{ID: "opencode", Available: true, Capabilities: protocol.AgentCapabilities{Streaming: true}},
	}
	result, err := routeCLI(infos, nil, routeCLIParams{
		Prefer: []string{" opencode ", " codex "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Agent.ID != "opencode" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRouteCLIAccountAutoSelectsFreshLowestQuotaCodex(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	if err := accounts.UpsertAccount(account.Account{
		ID:        "codex-low",
		Provider:  codexauth.Provider,
		AuthMode:  "chatgpt",
		Email:     "low@example.com",
		SecretRef: "file:codex-low",
	}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", PrimaryUsedPercent: 60}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-low", PrimaryUsedPercent: 3}); err != nil {
		t.Fatal(err)
	}
	infos := []protocol.AgentInfo{
		{ID: "codex", Available: true, Capabilities: protocol.AgentCapabilities{Usage: true, Resume: true}},
		{ID: "opencode", Available: true, Capabilities: protocol.AgentCapabilities{Usage: true, Resume: true}},
	}
	result, err := routeCLI(infos, accounts, routeCLIParams{AccountID: protocol.AccountAuto})
	if err != nil {
		t.Fatal(err)
	}
	if result.Agent.ID != "codex" || result.AccountID != "codex-low" {
		t.Fatalf("result = %+v", result)
	}
	if result.AccountRoute == nil || result.AccountRoute.AccountID != "codex-low" || result.AccountRoute.QuotaState != protocol.AccountQuotaStateFresh || !result.AccountRoute.Fresh || result.AccountRoute.PrimaryUsedPercent == nil || *result.AccountRoute.PrimaryUsedPercent != 3 {
		t.Fatalf("account route = %+v", result.AccountRoute)
	}
	if len(result.RouteCandidates) != 2 || result.RouteCandidates[0].AccountID != "codex-low" || result.RouteCandidates[1].AccountID != "codex-test" {
		t.Fatalf("route candidates = %+v", result.RouteCandidates)
	}
	if !strings.Contains(result.Reason, "primary 3%") {
		t.Fatalf("reason = %q", result.Reason)
	}
}

func TestRouteCLIAccountAutoRequireFreshQuotaFailsWhenMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	if err := accounts.UpsertAccount(account.Account{
		ID:        "codex-missing",
		Provider:  codexauth.Provider,
		AuthMode:  "chatgpt",
		Email:     "missing@example.com",
		SecretRef: "file:codex-missing",
	}); err != nil {
		t.Fatal(err)
	}
	staleAt := time.Now().Add(-account.QuotaRouteCacheTTL - time.Minute).Unix()
	if err := accounts.SaveQuota(account.QuotaSnapshot{
		AccountID:          "codex-test",
		PrimaryUsedPercent: 2,
		CheckedAt:          staleAt,
		RawJSON:            `{"token":"must-not-return"}`,
	}); err != nil {
		t.Fatal(err)
	}
	infos := []protocol.AgentInfo{
		{ID: "codex", Available: true, Capabilities: protocol.AgentCapabilities{Usage: true, Resume: true}},
	}
	_, err := routeCLI(infos, accounts, routeCLIParams{
		AccountID:    protocol.AccountAuto,
		RequireFresh: true,
	})
	if err == nil || !strings.Contains(err.Error(), "fresh cached quota") {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{
		"route: quota stale fresh false primary 2.0% score 74.99 checked",
		"route candidates: codex-test quota stale",
		"codex-missing quota missing",
		"next: refresh and verify daemon-side readiness with: capd accounts check --json --readiness --require-secret-backend file --timeout 2m",
		"next: preview routing with: capd agents route --account auto --require-fresh-quota --json",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %s", want, err.Error())
		}
	}
	for _, leaked := range []string{"must-not-return", "secretRef", "rawJson", "RawJSON"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("error leaked %q: %s", leaked, err.Error())
		}
	}
}

func TestRouteCLIAccountAutoRequireFreshQuotaPreservesEnvBackendInNextStep(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(secret.EnvBackend, secret.BackendFile)
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	staleAt := time.Now().Add(-account.QuotaRouteCacheTTL - time.Minute).Unix()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", PrimaryUsedPercent: 42, CheckedAt: staleAt}); err != nil {
		t.Fatal(err)
	}
	infos := []protocol.AgentInfo{
		{ID: "codex", Available: true, Capabilities: protocol.AgentCapabilities{Usage: true, Resume: true}},
	}
	_, err := routeCLI(infos, accounts, routeCLIParams{
		AccountID:    protocol.AccountAuto,
		RequireFresh: true,
	})
	if err == nil {
		t.Fatal("expected fresh quota error")
	}
	want := "next: refresh and verify daemon-side readiness with: capd accounts check --json --readiness --require-secret-backend file --timeout 2m"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error missing %q: %s", want, err.Error())
	}
}

func TestRouteCLIAccountAutoRequireFreshQuotaPrefersAccountSecretBackendInNextStep(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(secret.EnvBackend, secret.BackendFile)
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	acc, err := accounts.LoadAccount("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	acc.SecretRef = secret.BackendNative + ":codex-test"
	if err := accounts.UpsertAccount(acc); err != nil {
		t.Fatal(err)
	}
	staleAt := time.Now().Add(-account.QuotaRouteCacheTTL - time.Minute).Unix()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", PrimaryUsedPercent: 42, CheckedAt: staleAt}); err != nil {
		t.Fatal(err)
	}
	infos := []protocol.AgentInfo{
		{ID: "codex", Available: true, Capabilities: protocol.AgentCapabilities{Usage: true, Resume: true}},
	}
	_, err = routeCLI(infos, accounts, routeCLIParams{
		AccountID:    protocol.AccountAuto,
		RequireFresh: true,
	})
	if err == nil {
		t.Fatal("expected fresh quota error")
	}
	want := "next: refresh and verify daemon-side readiness with: capd accounts check --json --readiness --require-secret-backend native --timeout 2m"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error missing %q: %s", want, err.Error())
	}
	if strings.Contains(err.Error(), "--require-secret-backend file") {
		t.Fatalf("error used env backend instead of account backend: %s", err.Error())
	}
}

func TestRouteCLIAccountAutoRequireFreshQuotaPassesWithFreshCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
	if err := accounts.SaveQuota(account.QuotaSnapshot{AccountID: "codex-test", PrimaryUsedPercent: 8}); err != nil {
		t.Fatal(err)
	}
	infos := []protocol.AgentInfo{
		{ID: "codex", Available: true, Capabilities: protocol.AgentCapabilities{Usage: true, Resume: true}},
	}
	result, err := routeCLI(infos, accounts, routeCLIParams{
		AccountID:    protocol.AccountAuto,
		RequireFresh: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountID != "codex-test" || !strings.Contains(result.Reason, "primary 8%") {
		t.Fatalf("result = %+v", result)
	}
	if result.AccountRoute == nil || result.AccountRoute.AccountID != "codex-test" || result.AccountRoute.QuotaState != protocol.AccountQuotaStateFresh || result.AccountRoute.PrimaryUsedPercent == nil || *result.AccountRoute.PrimaryUsedPercent != 8 {
		t.Fatalf("account route = %+v", result.AccountRoute)
	}
	if len(result.RouteCandidates) != 1 || result.RouteCandidates[0].AccountID != "codex-test" {
		t.Fatalf("route candidates = %+v", result.RouteCandidates)
	}
}

func TestRouteCLITextIncludesAccountRouteEvidence(t *testing.T) {
	primary := 8.0
	text := routeCLIText(protocol.AgentRouteResult{
		Agent:     protocol.AgentInfo{ID: "codex"},
		AccountID: "codex-test",
		AccountRoute: &protocol.AccountRouteEvidence{
			AccountID:          "codex-test",
			QuotaState:         protocol.AccountQuotaStateFresh,
			Fresh:              true,
			PrimaryUsedPercent: &primary,
			Score:              8,
			CheckedAt:          1700000000,
		},
		Reason: "matched capabilities; auto account codex-test primary 8%",
	})
	for _, want := range []string{
		"codex\tcodex-test\tquota fresh fresh true primary 8.0% score 8.00 checked ",
		"matched capabilities",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("route text missing %q: %s", want, text)
		}
	}
}

func TestRouteCLIRejectsUnknownCapability(t *testing.T) {
	_, err := agentCapabilitiesFromNames([]string{"review", "telepathy"})
	if err == nil || !strings.Contains(err.Error(), "unknown capability") {
		t.Fatalf("err = %v", err)
	}
}

func TestSaveUsageQuotaBackfillsAccountMetadata(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, secrets := seedCodexAccount(t)
	defer accounts.Close()
	acc, err := accounts.LoadAccount("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	acc.Email = ""
	acc.AccountID = ""
	acc.Plan = ""
	if err := accounts.UpsertAccount(acc); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Put(context.Background(), "codex-test", secret.Bundle{
		Provider:    codexauth.Provider,
		AuthMode:    "chatgpt",
		AccessToken: "access-secret",
		AccountID:   "acct_usage",
		Email:       "usage@example.com",
		RawAuthJSON: []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"access-secret","account_id":"acct_usage"}}`),
	}); err != nil {
		t.Fatal(err)
	}
	usage := map[string]any{
		"planType": "enterprise",
		"rateLimits": map[string]any{
			"primary": map[string]any{"usedPercent": 31.0},
		},
	}
	if err := saveUsageQuota(context.Background(), accounts, secrets, acc, usage); err != nil {
		t.Fatal(err)
	}
	got, err := accounts.LoadAccount("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "usage@example.com" || got.AccountID != "acct_usage" || got.Plan != "enterprise" {
		t.Fatalf("stored account = %+v", got)
	}
	q, err := accounts.LoadQuota("codex-test")
	if err != nil {
		t.Fatal(err)
	}
	if q.Plan != "enterprise" || q.PrimaryUsedPercent != 31 {
		t.Fatalf("quota = %+v", q)
	}
}
