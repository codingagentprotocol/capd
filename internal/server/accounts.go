package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/internal/account/codexquota"
	"github.com/codingagentprotocol/capd/internal/account/secret"
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

func (s *Server) refreshAccountQuota(ctx context.Context, params protocol.AccountsQuotaParams) (protocol.AccountsQuotaResult, *protocol.Error) {
	if s.opts.Accounts == nil || s.opts.Secrets == nil {
		return protocol.AccountsQuotaResult{}, protocol.NewError(protocol.CodeInvalidParams, "account support is not configured")
	}
	provider := params.Provider
	if provider == "" {
		provider = codexauth.Provider
	}
	if provider != codexauth.Provider {
		return protocol.AccountsQuotaResult{}, protocol.NewError(protocol.CodeInvalidParams, "quota refresh is currently supported only for provider %q", codexauth.Provider)
	}
	accountID := strings.TrimSpace(params.AccountID)
	var err error
	if accountID == "" {
		accountID, err = s.opts.Accounts.CurrentAccount(provider)
		if err != nil {
			return protocol.AccountsQuotaResult{}, protocol.NewError(protocol.CodeInternalError, "load current account: %v", err)
		}
	}
	if accountID == "" {
		return protocol.AccountsQuotaResult{}, protocol.NewError(protocol.CodeInvalidParams, "accountId is required")
	}
	acc, err := s.opts.Accounts.LoadAccount(accountID)
	if err != nil {
		return protocol.AccountsQuotaResult{}, protocol.NewError(protocol.CodeInvalidParams, "unknown accountId %q", accountID)
	}
	if acc.Provider != codexauth.Provider {
		return protocol.AccountsQuotaResult{}, protocol.NewError(protocol.CodeInvalidParams, "accountId %q is not a Codex account", accountID)
	}
	ref, err := secret.ParseRef(acc.SecretRef)
	if err != nil {
		return protocol.AccountsQuotaResult{}, protocol.NewError(protocol.CodeInternalError, "parse secret ref: %v", err)
	}
	bundle, err := s.opts.Secrets.Get(ctx, ref)
	if err != nil {
		return protocol.AccountsQuotaResult{}, protocol.NewError(protocol.CodeInternalError, "load account secret: %v", err)
	}
	result, err := codexquota.Client{BaseURL: s.opts.CodexQuotaBaseURL}.Usage(ctx, acc.ID, bundle)
	if err != nil {
		return protocol.AccountsQuotaResult{}, protocol.NewError(protocol.CodeAgentUnavailable, "quota: %v", err)
	}
	if err := s.opts.Accounts.SaveQuota(result.Quota); err != nil {
		return protocol.AccountsQuotaResult{}, protocol.NewError(protocol.CodeInternalError, "save quota: %v", err)
	}
	summary := accountSummary(acc, result.Quota)
	return protocol.AccountsQuotaResult{Account: summary}, nil
}

func accountSummary(acc account.Account, quota account.QuotaSnapshot) protocol.AccountSummary {
	summary := protocol.AccountSummary{
		ID:        acc.ID,
		Provider:  acc.Provider,
		AuthMode:  acc.AuthMode,
		Email:     acc.Email,
		AccountID: acc.AccountID,
		Plan:      acc.Plan,
		Quota:     quotaSummary(quota),
	}
	if summary.Plan == "" {
		summary.Plan = quota.Plan
	}
	return summary
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
