package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

const codexAgentID = "codex"

func (s *Server) runtimeEnvForAccount(ctx context.Context, agentID, accountID string) ([]string, *protocol.Error) {
	if agentID != codexAgentID {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "accountId is currently supported only for agent %q", codexAgentID)
	}
	if s.opts.Accounts == nil || s.opts.Secrets == nil || s.opts.RuntimeRoot == "" {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "account support is not configured")
	}
	if strings.TrimSpace(accountID) == "" {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "accountId is required")
	}
	acc, err := s.opts.Accounts.LoadAccount(accountID)
	if err != nil {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "unknown accountId %q", accountID)
	}
	if acc.Provider != codexauth.Provider {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "accountId %q is not a Codex account", accountID)
	}
	profile, err := codexauth.RuntimeProjector{
		Root:    s.opts.RuntimeRoot,
		Secrets: s.opts.Secrets,
	}.Project(ctx, acc)
	if err != nil {
		return nil, protocol.NewError(protocol.CodeInternalError, "project account runtime: %v", err)
	}
	if len(profile.Env) == 0 {
		return nil, protocol.NewError(protocol.CodeInternalError, "%v", fmt.Errorf("empty runtime environment for account %q", accountID))
	}
	return profile.Env, nil
}

func (s *Server) listAccounts(params protocol.AccountsListParams) (protocol.AccountsListResult, *protocol.Error) {
	if s.opts.Accounts == nil {
		return protocol.AccountsListResult{Accounts: []protocol.AccountSummary{}}, nil
	}
	provider := params.Provider
	if provider == "" {
		provider = codexauth.Provider
	}
	current, err := s.opts.Accounts.CurrentAccount(provider)
	if err != nil {
		return protocol.AccountsListResult{}, protocol.NewError(protocol.CodeInternalError, "load current account: %v", err)
	}
	accounts, err := s.opts.Accounts.ListAccounts(provider)
	if err != nil {
		return protocol.AccountsListResult{}, protocol.NewError(protocol.CodeInternalError, "list accounts: %v", err)
	}
	result := protocol.AccountsListResult{
		CurrentAccountID: current,
		Accounts:         make([]protocol.AccountSummary, 0, len(accounts)),
	}
	for _, acc := range accounts {
		summary := protocol.AccountSummary{
			ID:        acc.ID,
			Provider:  acc.Provider,
			AuthMode:  acc.AuthMode,
			Email:     acc.Email,
			AccountID: acc.AccountID,
			Plan:      acc.Plan,
		}
		if q, err := s.opts.Accounts.LoadQuota(acc.ID); err == nil {
			summary.Quota = quotaSummary(q)
			if summary.Plan == "" {
				summary.Plan = q.Plan
			}
		}
		result.Accounts = append(result.Accounts, summary)
	}
	return result, nil
}

func quotaSummary(q account.QuotaSnapshot) *protocol.AccountQuotaSnapshot {
	return &protocol.AccountQuotaSnapshot{
		Plan:                  q.Plan,
		PrimaryUsedPercent:    q.PrimaryUsedPercent,
		PrimaryResetAt:        q.PrimaryResetAt,
		SecondaryUsedPercent:  q.SecondaryUsedPercent,
		SecondaryResetAt:      q.SecondaryResetAt,
		CodeReviewUsedPercent: q.CodeReviewUsedPercent,
		CheckedAt:             q.CheckedAt,
	}
}
