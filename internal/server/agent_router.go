package server

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/internal/account/secret"
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
	model := strings.TrimSpace(params.Model)
	effort := strings.TrimSpace(params.Effort)
	if model != "" {
		required.Model = true
	}
	if effort != "" {
		required.Effort = true
	}
	return protocol.AgentRouteParams{
		Model:             model,
		Effort:            effort,
		AccountID:         strings.TrimSpace(params.AccountID),
		Profile:           strings.TrimSpace(params.Profile),
		Capabilities:      required,
		RequireFreshQuota: params.RequireFreshQuota,
	}
}

func (s *Server) routeAgent(ctx context.Context, params protocol.AgentRouteParams) (result protocol.AgentRouteResult, perr *protocol.Error) {
	defer func() {
		if perr != nil {
			s.metrics.recordRouteDecision("", false)
			s.recordRouteAudit(params, protocol.AgentRouteResult{}, perr)
			return
		}
		s.metrics.recordRouteDecision(result.Agent.ID, true)
		s.recordRouteAudit(params, result, nil)
	}()
	params.Model = strings.TrimSpace(params.Model)
	params.Effort = strings.TrimSpace(params.Effort)
	params.AccountID = strings.TrimSpace(params.AccountID)
	params.Profile = strings.TrimSpace(params.Profile)
	params.Prefer = trimRoutePreference(params.Prefer)
	required := routeRequirements(params)
	prefer := params.Prefer
	if len(prefer) == 0 {
		prefer = defaultRoutePreference
	}
	accountID := params.AccountID
	if params.RequireFreshQuota && accountID != protocol.AccountAuto {
		return protocol.AgentRouteResult{}, protocol.NewError(protocol.CodeInvalidParams, "requireFreshQuota is supported only with accountId %q", protocol.AccountAuto)
	}
	if params.Profile != "" && accountID != protocol.AccountAuto {
		return protocol.AgentRouteResult{}, protocol.NewError(protocol.CodeInvalidParams, "profile is supported only with accountId %q", protocol.AccountAuto)
	}
	if perr := rejectReservedAccountID(accountID); perr != nil {
		return protocol.AgentRouteResult{}, perr
	}
	var selectedAccountID string
	var accountReason string
	var selectedAccount account.Account
	if accountID != "" {
		prefer = []string{codexAgentID}
		required.Usage = true
		required.Resume = true
		if accountID == protocol.AccountAuto {
			acc, reason, perr := s.selectCodexAccountForRoute(params.Profile)
			if perr != nil {
				return protocol.AgentRouteResult{}, perr
			}
			if params.RequireFreshQuota {
				if q, err := s.opts.Accounts.LoadQuota(acc.ID); err != nil || !account.QuotaSnapshotFresh(q, time.Now()) {
					return protocol.AgentRouteResult{}, s.routeFreshQuotaError(acc, params.Profile)
				}
			}
			selectedAccountID = acc.ID
			selectedAccount = acc
			accountReason = reason
		} else {
			account, perr := s.loadCodexAccountForRoute(accountID)
			if perr != nil {
				return protocol.AgentRouteResult{}, perr
			}
			selectedAccountID = accountID
			selectedAccount = account
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
	result = protocol.AgentRouteResult{Agent: best, AccountID: selectedAccountID, Reason: reason}
	if selectedAccount.ID != "" && s.opts.Accounts != nil {
		evidence := account.QuotaRouteEvidence(s.opts.Accounts, selectedAccount)
		result.AccountRoute = &evidence
		policy := account.DefaultRoutePolicyEvidence()
		result.RoutePolicy = &policy
		if candidates, err := s.codexRouteCandidates(params.Profile); err == nil {
			result.RouteCandidates = candidates
		}
	}
	return result, nil
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

func (s *Server) selectCodexAccountForRoute(profile string) (account.Account, string, *protocol.Error) {
	if s.opts.Accounts == nil {
		return account.Account{}, "", protocol.NewError(protocol.CodeInvalidParams, "account support is not configured")
	}
	profile = strings.TrimSpace(profile)
	var best account.Account
	var err error
	if profile == "" {
		best, err = account.SelectQuotaRouteAccount(s.opts.Accounts, codexauth.Provider)
	} else {
		best, err = s.selectCodexProfileAccountForRoute(profile)
	}
	if err != nil {
		if profile != "" {
			return account.Account{}, "", protocol.NewError(protocol.CodeInvalidParams, "no Codex accounts in profile %q", profile)
		}
		return account.Account{}, "", protocol.NewError(protocol.CodeInvalidParams, "no imported Codex accounts")
	}
	reason := account.QuotaRouteReason(s.opts.Accounts, best)
	if profile != "" {
		reason += "; profile " + profile
	}
	return best, reason, nil
}

func (s *Server) selectCodexProfileAccountForRoute(profile string) (account.Account, error) {
	members, err := s.opts.Accounts.ProfileAccounts(codexauth.Provider, profile)
	if err != nil {
		return account.Account{}, err
	}
	if len(members) == 0 {
		return account.Account{}, account.ErrUnknownAccount
	}
	current, _ := s.opts.Accounts.CurrentAccount(codexauth.Provider)
	best := members[0]
	bestScore := account.QuotaRouteScore(s.opts.Accounts, best, current)
	for _, member := range members[1:] {
		score := account.QuotaRouteScore(s.opts.Accounts, member, current)
		if score < bestScore || (score == bestScore && member.ID < best.ID) {
			best = member
			bestScore = score
		}
	}
	return best, nil
}

func (s *Server) codexRouteCandidates(profile string) ([]protocol.AccountRouteEvidence, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return account.QuotaRouteCandidates(s.opts.Accounts, codexauth.Provider)
	}
	members, err := s.opts.Accounts.ProfileAccounts(codexauth.Provider, profile)
	if err != nil {
		return nil, err
	}
	current, _ := s.opts.Accounts.CurrentAccount(codexauth.Provider)
	sort.Slice(members, func(i, j int) bool {
		leftScore := account.QuotaRouteScore(s.opts.Accounts, members[i], current)
		rightScore := account.QuotaRouteScore(s.opts.Accounts, members[j], current)
		if leftScore != rightScore {
			return leftScore < rightScore
		}
		return members[i].ID < members[j].ID
	})
	candidates := make([]protocol.AccountRouteEvidence, 0, len(members))
	for _, member := range members {
		candidates = append(candidates, account.QuotaRouteEvidence(s.opts.Accounts, member))
	}
	return candidates, nil
}

func (s *Server) routeFreshQuotaError(acc account.Account, profile string) *protocol.Error {
	perr := protocol.NewError(protocol.CodeInvalidParams, freshQuotaRefreshHint)
	if s.opts.Accounts == nil || acc.ID == "" {
		return perr
	}
	evidence := account.QuotaRouteEvidence(s.opts.Accounts, acc)
	policy := account.DefaultRoutePolicyEvidence()
	data := protocol.AgentRouteErrorData{AccountRoute: &evidence, RoutePolicy: &policy}
	if ref, err := secret.ParseRef(acc.SecretRef); err == nil {
		data.SecretBackend = ref.Backend
	}
	if candidates, err := s.codexRouteCandidates(profile); err == nil {
		data.RouteCandidates = candidates
	}
	perr.Data = data
	return perr
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

func trimRoutePreference(prefer []string) []string {
	if len(prefer) == 0 {
		return nil
	}
	out := make([]string, 0, len(prefer))
	for _, raw := range prefer {
		if item := strings.TrimSpace(raw); item != "" {
			out = append(out, item)
		}
	}
	return out
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
