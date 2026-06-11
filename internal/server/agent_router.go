package server

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/internal/discovery"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

var defaultRoutePreference = []string{
	"codex",
	"claude-code",
	"opencode",
	"gemini",
	"cursor-agent",
}

func routeParamsForCreate(params protocol.SessionCreateParams) protocol.AgentRouteParams {
	required := protocol.AgentCapabilities{}
	if params.Model != "" {
		required.Model = true
	}
	if params.Effort != "" {
		required.Effort = true
	}
	return protocol.AgentRouteParams{
		Model:        params.Model,
		Effort:       params.Effort,
		AccountID:    params.AccountID,
		Capabilities: required,
	}
}

func (s *Server) routeAgent(ctx context.Context, params protocol.AgentRouteParams) (protocol.AgentRouteResult, *protocol.Error) {
	required := routeRequirements(params)
	prefer := params.Prefer
	if len(prefer) == 0 {
		prefer = defaultRoutePreference
	}
	accountID := strings.TrimSpace(params.AccountID)
	var selectedAccountID string
	var accountReason string
	if accountID != "" {
		prefer = []string{codexAgentID}
		required.Usage = true
		required.Resume = true
		if accountID == protocol.AccountAuto {
			account, reason, perr := s.selectCodexAccountForRoute()
			if perr != nil {
				return protocol.AgentRouteResult{}, perr
			}
			selectedAccountID = account.ID
			accountReason = reason
		} else {
			if _, perr := s.loadCodexAccountForRoute(accountID); perr != nil {
				return protocol.AgentRouteResult{}, perr
			}
			selectedAccountID = accountID
			accountReason = "explicit accountId"
		}
	}

	var best protocol.AgentInfo
	bestScore := -1
	for _, agent := range discovery.Discover(ctx, s.opts.Registry) {
		if accountID != "" && agent.ID != codexAgentID {
			continue
		}
		if !agent.Available || !hasCapabilities(agent.Capabilities, required) {
			continue
		}
		score := routeScore(agent, prefer)
		if score > bestScore {
			best = agent
			bestScore = score
		}
	}
	if bestScore < 0 {
		return protocol.AgentRouteResult{}, protocol.NewError(
			protocol.CodeAgentUnavailable, "no available agent satisfies requested capabilities")
	}
	reason := fmt.Sprintf("matched capabilities%s", routeReasonSuffix(required))
	if accountID != "" {
		reason += "; accountId requires codex account runtime"
		if accountReason != "" {
			reason += "; " + accountReason
		}
	}
	return protocol.AgentRouteResult{Agent: best, AccountID: selectedAccountID, Reason: reason}, nil
}

func (s *Server) loadCodexAccountForRoute(accountID string) (account.Account, *protocol.Error) {
	if s.opts.Accounts == nil {
		return account.Account{}, protocol.NewError(protocol.CodeInvalidParams, "account support is not configured")
	}
	acc, err := s.opts.Accounts.LoadAccount(accountID)
	if err != nil {
		return account.Account{}, protocol.NewError(protocol.CodeInvalidParams, "unknown accountId %q", accountID)
	}
	if acc.Provider != codexauth.Provider {
		return account.Account{}, protocol.NewError(protocol.CodeInvalidParams, "accountId %q is not a Codex account", accountID)
	}
	return acc, nil
}

func (s *Server) selectCodexAccountForRoute() (account.Account, string, *protocol.Error) {
	if s.opts.Accounts == nil {
		return account.Account{}, "", protocol.NewError(protocol.CodeInvalidParams, "account support is not configured")
	}
	best, err := account.SelectLowestQuotaAccount(s.opts.Accounts, codexauth.Provider)
	if err != nil {
		return account.Account{}, "", protocol.NewError(protocol.CodeInvalidParams, "no imported Codex accounts")
	}
	if q, err := s.opts.Accounts.LoadQuota(best.ID); err == nil {
		return best, fmt.Sprintf("auto account %s primary %.0f%%", best.ID, q.PrimaryUsedPercent), nil
	}
	return best, fmt.Sprintf("auto account %s without cached quota", best.ID), nil
}

func routeRequirements(params protocol.AgentRouteParams) protocol.AgentCapabilities {
	required := params.Capabilities
	if params.Model != "" {
		required.Model = true
	}
	if params.Effort != "" {
		required.Effort = true
	}
	if len(params.Attachments) > 0 {
		required.Images = true
	}
	return required
}

func hasCapabilities(got, want protocol.AgentCapabilities) bool {
	return (!want.Model || got.Model) &&
		(!want.Effort || got.Effort) &&
		(!want.Streaming || got.Streaming) &&
		(!want.Approvals || got.Approvals) &&
		(!want.Steer || got.Steer) &&
		(!want.Fork || got.Fork) &&
		(!want.Rollback || got.Rollback) &&
		(!want.Review || got.Review) &&
		(!want.Images || got.Images) &&
		(!want.Usage || got.Usage) &&
		(!want.Resume || got.Resume)
}

func routeScore(agent protocol.AgentInfo, prefer []string) int {
	score := countCapabilities(agent.Capabilities)
	if idx := slices.Index(prefer, agent.ID); idx >= 0 {
		score += 1000 - idx
	}
	return score
}

func countCapabilities(c protocol.AgentCapabilities) int {
	n := 0
	for _, enabled := range []bool{
		c.Model, c.Effort, c.Streaming, c.Approvals, c.Steer, c.Fork,
		c.Rollback, c.Review, c.Images, c.Usage, c.Resume,
	} {
		if enabled {
			n++
		}
	}
	return n
}

func routeReasonSuffix(required protocol.AgentCapabilities) string {
	var names []string
	if required.Model {
		names = append(names, "model")
	}
	if required.Effort {
		names = append(names, "effort")
	}
	if required.Streaming {
		names = append(names, "streaming")
	}
	if required.Approvals {
		names = append(names, "approvals")
	}
	if required.Steer {
		names = append(names, "steer")
	}
	if required.Fork {
		names = append(names, "fork")
	}
	if required.Rollback {
		names = append(names, "rollback")
	}
	if required.Review {
		names = append(names, "review")
	}
	if required.Images {
		names = append(names, "images")
	}
	if required.Usage {
		names = append(names, "usage")
	}
	if required.Resume {
		names = append(names, "resume")
	}
	if len(names) == 0 {
		return ""
	}
	return ": " + strings.Join(names, ", ")
}
