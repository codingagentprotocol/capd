package main

import (
	"testing"

	"github.com/codingagentprotocol/capd/internal/audit"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestRecordAgentRouteAuditWritesSafeRouteMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	route := protocol.AccountRouteEvidence{
		AccountID:  "codex-low",
		QuotaState: protocol.AccountQuotaStateFresh,
		Fresh:      true,
	}
	recordAgentRouteAudit(
		routeCLIParams{AccountID: protocol.AccountAuto, Profile: "work", TaskClass: "long-running", RequireFresh: true},
		protocol.AgentRouteResult{
			Agent:           protocol.AgentInfo{ID: "codex"},
			AccountID:       "codex-low",
			AccountRoute:    &route,
			RouteCandidates: []protocol.AccountRouteEvidence{{AccountID: "codex-low"}, {AccountID: "codex-high"}},
		},
		nil,
	)
	events, err := audit.Recent("", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	ev := events[0]
	if ev.Type != "agents.route" || ev.Actor != "cli" || ev.Outcome != "ok" {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Data["agent"] != "codex" || ev.Data["account"] != "codex-low" || ev.Data["accountMode"] != protocol.AccountAuto || ev.Data["profile"] != "work" || ev.Data["taskClass"] != "long-running" || ev.Data["quotaState"] != string(protocol.AccountQuotaStateFresh) || ev.Data["quotaFresh"] != true || ev.Data["routeCandidates"] != float64(2) {
		t.Fatalf("data = %+v", ev.Data)
	}
}

func TestRecordSecretStoreCheckAuditWritesSafeOutcome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	recordSecretStoreCheckAudit(secretStoreReport{
		OK:              false,
		Backend:         "file",
		RequiredBackend: "native",
		RoundTrip:       &secretStoreCheck{Name: "roundtrip"},
		Checks:          []secretStoreCheck{{Name: "backend"}, {Name: "roundtrip"}},
		Issues:          []string{"secret backend is file"},
	})
	events, err := audit.Recent("", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	ev := events[0]
	if ev.Type != "secretstore.check" || ev.Outcome != "failed" {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Data["backend"] != "file" || ev.Data["requiredBackend"] != "native" || ev.Data["roundTrip"] != true || ev.Data["checks"] != float64(2) || ev.Data["issues"] != float64(1) {
		t.Fatalf("data = %+v", ev.Data)
	}
}
