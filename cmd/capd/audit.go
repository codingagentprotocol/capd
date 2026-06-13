package main

import (
	"github.com/codingagentprotocol/capd/internal/audit"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func recordAuditEvent(ev audit.Event) {
	_ = audit.Append("", ev)
}

func recordAgentRouteAudit(params routeCLIParams, result protocol.AgentRouteResult, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	data := map[string]any{
		"accountMode":       params.AccountID,
		"profile":           params.Profile,
		"requireFreshQuota": params.RequireFresh,
	}
	if result.Agent.ID != "" {
		data["agent"] = result.Agent.ID
	}
	if result.AccountID != "" {
		data["account"] = result.AccountID
	}
	if result.AccountRoute != nil {
		data["quotaState"] = string(result.AccountRoute.QuotaState)
		data["quotaFresh"] = result.AccountRoute.Fresh
	}
	if len(result.RouteCandidates) > 0 {
		data["routeCandidates"] = int64(len(result.RouteCandidates))
	}
	recordAuditEvent(audit.Event{
		Type:    "agents.route",
		Actor:   "cli",
		Outcome: outcome,
		Data:    data,
	})
}

func recordSecretStoreCheckAudit(report secretStoreReport) {
	outcome := "ok"
	if !report.OK {
		outcome = "failed"
	}
	recordAuditEvent(audit.Event{
		Type:    "secretstore.check",
		Actor:   "cli",
		Outcome: outcome,
		Data: map[string]any{
			"backend":         report.Backend,
			"requiredBackend": report.RequiredBackend,
			"roundTrip":       report.RoundTrip != nil,
			"checks":          int64(len(report.Checks)),
			"issues":          int64(len(report.Issues)),
		},
	})
}
