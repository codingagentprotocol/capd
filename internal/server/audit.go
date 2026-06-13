package server

import (
	"github.com/codingagentprotocol/capd/internal/audit"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func (s *Server) recordAuditEvent(ev audit.Event) {
	_ = audit.Append("", ev)
}

func (s *Server) recordRouteAudit(params protocol.AgentRouteParams, result protocol.AgentRouteResult, perr *protocol.Error) {
	outcome := "ok"
	if perr != nil {
		outcome = "failed"
	}
	data := map[string]any{
		"accountMode":       params.AccountID,
		"profile":           params.Profile,
		"requireFreshQuota": params.RequireFreshQuota,
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
	s.recordAuditEvent(audit.Event{
		Type:    "agents.route",
		Actor:   "daemon",
		Outcome: outcome,
		Data:    data,
	})
}

func (s *Server) recordAccountImportAudit(outcome, provider, backend, accountID, authMode string) {
	s.recordAuditEvent(audit.Event{
		Type:    "accounts.import",
		Actor:   "daemon",
		Outcome: outcome,
		Data: map[string]any{
			"provider": provider,
			"backend":  backend,
			"account":  accountID,
			"authMode": authMode,
		},
	})
}

func (s *Server) recordApprovalAudit(params protocol.ApprovalReplyParams, outcome string) {
	s.recordAuditEvent(audit.Event{
		Type:    "approval.reply",
		Actor:   "daemon",
		Outcome: outcome,
		Data: map[string]any{
			"session":  params.SessionID,
			"decision": params.Decision,
		},
	})
}
