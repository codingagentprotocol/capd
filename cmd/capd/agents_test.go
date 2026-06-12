package main

import (
	"strings"
	"testing"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
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
	if !strings.Contains(result.Reason, "primary 3%") {
		t.Fatalf("reason = %q", result.Reason)
	}
}

func TestRouteCLIAccountAutoRequireFreshQuotaFailsWhenMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	accounts, _ := seedCodexAccount(t)
	defer accounts.Close()
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
}

func TestRouteCLIRejectsUnknownCapability(t *testing.T) {
	_, err := agentCapabilitiesFromNames([]string{"review", "telepathy"})
	if err == nil || !strings.Contains(err.Error(), "unknown capability") {
		t.Fatalf("err = %v", err)
	}
}
