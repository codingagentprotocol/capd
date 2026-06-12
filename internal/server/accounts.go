package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

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
	unlock := s.lockAccountRuntime(acc.ID)
	defer unlock()
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

func (s *Server) lockAccountRuntime(accountID string) func() {
	s.accountMu.Lock()
	mu := s.accountMux[accountID]
	if mu == nil {
		mu = &sync.Mutex{}
		s.accountMux[accountID] = mu
	}
	s.accountMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func (s *Server) listAccounts(params protocol.AccountsListParams) (protocol.AccountsListResult, *protocol.Error) {
	if s.opts.Accounts == nil {
		return protocol.AccountsListResult{Accounts: []protocol.AccountSummary{}}, nil
	}
	provider := params.Provider
	var current string
	if provider != "" {
		var err error
		current, err = s.opts.Accounts.CurrentAccount(provider)
		if err != nil {
			return protocol.AccountsListResult{}, protocol.NewError(protocol.CodeInternalError, "load current account: %v", err)
		}
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

func (s *Server) importAccount(ctx context.Context, params protocol.AccountsImportParams) (protocol.AccountsImportResult, *protocol.Error) {
	if s.opts.Accounts == nil || s.opts.Secrets == nil {
		return protocol.AccountsImportResult{}, protocol.NewError(protocol.CodeInvalidParams, "account support is not configured")
	}
	provider := strings.TrimSpace(params.Provider)
	if provider == "" {
		provider = codexauth.Provider
	}
	if provider != codexauth.Provider {
		return protocol.AccountsImportResult{}, protocol.NewError(protocol.CodeInvalidParams, "account import is currently supported only for provider %q", codexauth.Provider)
	}
	authPath := strings.TrimSpace(params.AuthPath)
	if authPath == "" {
		var err error
		authPath, err = codexauth.DefaultAuthPath("")
		if err != nil {
			return protocol.AccountsImportResult{}, protocol.NewError(protocol.CodeInternalError, "default auth path: %v", err)
		}
	}
	result, err := codexauth.Importer{Accounts: s.opts.Accounts, Secrets: s.opts.Secrets}.ImportAuthJSON(ctx, authPath)
	if err != nil {
		return protocol.AccountsImportResult{}, protocol.NewError(protocol.CodeInvalidParams, "import account: %s", safeImportError(err, authPath))
	}
	current, err := s.opts.Accounts.CurrentAccount(provider)
	if err != nil {
		return protocol.AccountsImportResult{}, protocol.NewError(protocol.CodeInternalError, "load current account: %v", err)
	}
	return protocol.AccountsImportResult{
		CurrentAccountID: current,
		Account:          accountSummary(result.Account, account.QuotaSnapshot{}),
	}, nil
}

func safeImportError(err error, authPath string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if authPath != "" && strings.Contains(msg, authPath) {
		return "read auth json failed"
	}
	return msg
}

func (s *Server) currentAccount(params protocol.AccountsCurrentParams) (protocol.AccountsCurrentResult, *protocol.Error) {
	if s.opts.Accounts == nil {
		return protocol.AccountsCurrentResult{}, nil
	}
	provider := strings.TrimSpace(params.Provider)
	if provider == "" {
		provider = codexauth.Provider
	}
	accountID := strings.TrimSpace(params.AccountID)
	if accountID != "" {
		acc, err := s.opts.Accounts.LoadAccount(accountID)
		if err != nil {
			return protocol.AccountsCurrentResult{}, protocol.NewError(protocol.CodeInvalidParams, "unknown accountId %q", accountID)
		}
		if acc.Provider != provider {
			return protocol.AccountsCurrentResult{}, protocol.NewError(protocol.CodeInvalidParams, "accountId %q is not a %s account", accountID, provider)
		}
		if err := s.opts.Accounts.SetCurrentAccount(provider, accountID); err != nil {
			return protocol.AccountsCurrentResult{}, protocol.NewError(protocol.CodeInternalError, "set current account: %v", err)
		}
		summary := accountSummary(acc, account.QuotaSnapshot{})
		if q, err := s.opts.Accounts.LoadQuota(acc.ID); err == nil {
			summary = accountSummary(acc, q)
		}
		return protocol.AccountsCurrentResult{CurrentAccountID: accountID, Account: &summary}, nil
	}
	current, err := s.opts.Accounts.CurrentAccount(provider)
	if err != nil {
		return protocol.AccountsCurrentResult{}, protocol.NewError(protocol.CodeInternalError, "load current account: %v", err)
	}
	if current == "" {
		return protocol.AccountsCurrentResult{}, nil
	}
	acc, err := s.opts.Accounts.LoadAccount(current)
	if err != nil {
		return protocol.AccountsCurrentResult{}, protocol.NewError(protocol.CodeInternalError, "load current account metadata: %v", err)
	}
	summary := accountSummary(acc, account.QuotaSnapshot{})
	if q, err := s.opts.Accounts.LoadQuota(acc.ID); err == nil {
		summary = accountSummary(acc, q)
	}
	return protocol.AccountsCurrentResult{CurrentAccountID: current, Account: &summary}, nil
}

func (s *Server) projectAccountRuntime(ctx context.Context, params protocol.AccountsProjectParams) (protocol.AccountsProjectResult, *protocol.Error) {
	provider := strings.TrimSpace(params.Provider)
	if provider == "" {
		provider = codexauth.Provider
	}
	if provider != codexauth.Provider {
		return protocol.AccountsProjectResult{}, protocol.NewError(protocol.CodeInvalidParams, "account projection is currently supported only for provider %q", codexauth.Provider)
	}
	accountID := strings.TrimSpace(params.AccountID)
	var err error
	if accountID == "" {
		if s.opts.Accounts == nil {
			return protocol.AccountsProjectResult{}, protocol.NewError(protocol.CodeInvalidParams, "account support is not configured")
		}
		accountID, err = s.opts.Accounts.CurrentAccount(provider)
		if err != nil {
			return protocol.AccountsProjectResult{}, protocol.NewError(protocol.CodeInternalError, "load current account: %v", err)
		}
	}
	if accountID == "" {
		return protocol.AccountsProjectResult{}, protocol.NewError(protocol.CodeInvalidParams, "accountId is required")
	}
	env, perr := s.runtimeEnvForAccount(ctx, codexAgentID, accountID)
	if perr != nil {
		return protocol.AccountsProjectResult{}, perr
	}
	codexHome := codexHomeFromEnv(env)
	if codexHome == "" {
		return protocol.AccountsProjectResult{}, protocol.NewError(protocol.CodeInternalError, "project account runtime: CODEX_HOME missing")
	}
	evidence, err := codexauth.VerifyRuntimeProfile(codexauth.RuntimeProfile{
		AccountID: accountID,
		CodexHome: codexHome,
		Env:       env,
	})
	if err != nil {
		return protocol.AccountsProjectResult{}, protocol.NewError(protocol.CodeInternalError, "verify account runtime: %v", err)
	}
	return protocol.AccountsProjectResult{
		AccountID:          accountID,
		RuntimeReady:       evidence.RuntimeEnvOK && evidence.AuthJSONPrivate && evidence.ProjectionMarkerOK,
		AuthJSONPrivate:    evidence.AuthJSONPrivate,
		ProjectionMarkerOK: evidence.ProjectionMarkerOK,
	}, nil
}

func codexHomeFromEnv(env []string) string {
	for _, entry := range env {
		if strings.HasPrefix(entry, "CODEX_HOME=") {
			return strings.TrimPrefix(entry, "CODEX_HOME=")
		}
	}
	return ""
}

func (s *Server) checkAccounts(ctx context.Context, params protocol.AccountsCheckParams) (protocol.AccountsCheckResult, *protocol.Error) {
	if s.opts.Accounts == nil || s.opts.Secrets == nil || s.opts.RuntimeRoot == "" {
		return protocol.AccountsCheckResult{}, protocol.NewError(protocol.CodeInvalidParams, "account support is not configured")
	}
	provider := strings.TrimSpace(params.Provider)
	if provider == "" {
		provider = codexauth.Provider
	}
	if provider != codexauth.Provider {
		return protocol.AccountsCheckResult{}, protocol.NewError(protocol.CodeInvalidParams, "account check is currently supported only for provider %q", codexauth.Provider)
	}
	current, err := s.opts.Accounts.CurrentAccount(provider)
	if err != nil {
		return protocol.AccountsCheckResult{}, protocol.NewError(protocol.CodeInternalError, "load current account: %v", err)
	}
	accounts, err := s.opts.Accounts.ListAccounts(provider)
	if err != nil {
		return protocol.AccountsCheckResult{}, protocol.NewError(protocol.CodeInternalError, "list accounts: %v", err)
	}
	result := protocol.AccountsCheckResult{
		Provider:         provider,
		CurrentAccountID: current,
		SecretBackend:    s.opts.Secrets.Backend(),
		CheckedAccounts:  len(accounts),
		Accounts:         make([]protocol.AccountCheckEvidence, 0, len(accounts)),
	}
	for _, acc := range accounts {
		row, perr := s.checkAccount(ctx, acc, current)
		if perr != nil {
			return protocol.AccountsCheckResult{}, perr
		}
		result.Accounts = append(result.Accounts, row)
	}
	if len(accounts) > 0 {
		selected, _, perr := s.selectCodexAccountForRoute()
		if perr != nil {
			return protocol.AccountsCheckResult{}, perr
		}
		evidence := account.QuotaRouteEvidence(s.opts.Accounts, selected)
		result.AutoRoute = &evidence
	}
	return result, nil
}

func (s *Server) checkAccount(ctx context.Context, acc account.Account, current string) (protocol.AccountCheckEvidence, *protocol.Error) {
	ref, err := secret.ParseRef(acc.SecretRef)
	if err != nil {
		return protocol.AccountCheckEvidence{}, protocol.NewError(protocol.CodeInternalError, "parse secret ref: %v", err)
	}
	if err := secret.EnsureRefBackend(s.opts.Secrets, ref); err != nil {
		return protocol.AccountCheckEvidence{}, protocol.NewError(protocol.CodeInternalError, "%v", err)
	}
	if _, err := s.opts.Secrets.Get(ctx, ref); err != nil {
		return protocol.AccountCheckEvidence{}, protocol.NewError(protocol.CodeInternalError, "load account credentials: %v", err)
	}
	env, perr := s.runtimeEnvForAccount(ctx, codexAgentID, acc.ID)
	if perr != nil {
		return protocol.AccountCheckEvidence{}, perr
	}
	codexHome := codexHomeFromEnv(env)
	if codexHome == "" {
		return protocol.AccountCheckEvidence{}, protocol.NewError(protocol.CodeInternalError, "project account runtime: CODEX_HOME missing")
	}
	evidence, err := codexauth.VerifyRuntimeProfile(codexauth.RuntimeProfile{
		AccountID: acc.ID,
		CodexHome: codexHome,
		Env:       env,
	})
	if err != nil {
		return protocol.AccountCheckEvidence{}, protocol.NewError(protocol.CodeInternalError, "verify account runtime: %v", err)
	}
	row := protocol.AccountCheckEvidence{
		ID:                 acc.ID,
		Email:              acc.Email,
		Current:            acc.ID == current,
		SecretBackendOK:    true,
		CredentialReadable: true,
		RuntimeReady:       evidence.RuntimeEnvOK && evidence.AuthJSONPrivate && evidence.ProjectionMarkerOK,
		AuthJSONPrivate:    evidence.AuthJSONPrivate,
		ProjectionMarkerOK: evidence.ProjectionMarkerOK,
		QuotaState:         protocol.AccountQuotaStateMissing,
	}
	if q, err := s.opts.Accounts.LoadQuota(acc.ID); err == nil {
		row.QuotaState = accountQuotaState(q)
		row.QuotaCheckedAt = q.CheckedAt
		row.PrimaryUsedPercent = &q.PrimaryUsedPercent
		row.QuotaFresh = row.QuotaState == protocol.AccountQuotaStateFresh
	}
	return row, nil
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
	} else if accountID == protocol.AccountAuto {
		acc, _, perr := s.selectCodexAccountForRoute()
		if perr != nil {
			return protocol.AccountsQuotaResult{}, perr
		}
		accountID = acc.ID
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
	if err := secret.EnsureRefBackend(s.opts.Secrets, ref); err != nil {
		return protocol.AccountsQuotaResult{}, protocol.NewError(protocol.CodeInternalError, "%v", err)
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

func (s *Server) removeAccount(ctx context.Context, params protocol.AccountsRemoveParams) (protocol.AccountsRemoveResult, *protocol.Error) {
	if s.opts.Accounts == nil || s.opts.Secrets == nil || s.opts.RuntimeRoot == "" {
		return protocol.AccountsRemoveResult{}, protocol.NewError(protocol.CodeInvalidParams, "account support is not configured")
	}
	provider := strings.TrimSpace(params.Provider)
	if provider == "" {
		provider = codexauth.Provider
	}
	if provider != codexauth.Provider {
		return protocol.AccountsRemoveResult{}, protocol.NewError(protocol.CodeInvalidParams, "account removal is currently supported only for provider %q", codexauth.Provider)
	}
	accountID := strings.TrimSpace(params.AccountID)
	if accountID == "" {
		return protocol.AccountsRemoveResult{}, protocol.NewError(protocol.CodeInvalidParams, "accountId is required")
	}
	acc, err := s.opts.Accounts.LoadAccount(accountID)
	if err != nil {
		return protocol.AccountsRemoveResult{}, protocol.NewError(protocol.CodeInvalidParams, "unknown accountId %q", accountID)
	}
	if acc.Provider != codexauth.Provider {
		return protocol.AccountsRemoveResult{}, protocol.NewError(protocol.CodeInvalidParams, "accountId %q is not a Codex account", accountID)
	}
	ref, err := secret.ParseRef(acc.SecretRef)
	if err != nil {
		return protocol.AccountsRemoveResult{}, protocol.NewError(protocol.CodeInternalError, "parse secret ref: %v", err)
	}
	if err := secret.EnsureRefBackend(s.opts.Secrets, ref); err != nil {
		return protocol.AccountsRemoveResult{}, protocol.NewError(protocol.CodeInternalError, "%v", err)
	}
	unlock := s.lockAccountRuntime(acc.ID)
	defer unlock()
	runtimeRemoved, err := codexauth.RemoveRuntimeProjection(s.opts.RuntimeRoot, acc)
	if err != nil {
		return protocol.AccountsRemoveResult{}, protocol.NewError(protocol.CodeInternalError, "remove account runtime: %v", err)
	}
	if err := s.opts.Secrets.Delete(ctx, ref); err != nil {
		return protocol.AccountsRemoveResult{}, protocol.NewError(protocol.CodeInternalError, "remove account credentials: %v", err)
	}
	if err := s.opts.Accounts.DeleteAccount(acc.ID); err != nil {
		return protocol.AccountsRemoveResult{}, protocol.NewError(protocol.CodeInternalError, "remove account metadata: %v", err)
	}
	current, err := s.opts.Accounts.CurrentAccount(provider)
	if err != nil {
		return protocol.AccountsRemoveResult{}, protocol.NewError(protocol.CodeInternalError, "load current account: %v", err)
	}
	remaining, err := s.opts.Accounts.ListAccounts(provider)
	if err != nil {
		return protocol.AccountsRemoveResult{}, protocol.NewError(protocol.CodeInternalError, "list accounts: %v", err)
	}
	return protocol.AccountsRemoveResult{
		AccountID:         acc.ID,
		RuntimeRemoved:    runtimeRemoved,
		CredentialRemoved: true,
		CurrentAccountID:  current,
		RemainingAccounts: len(remaining),
	}, nil
}

func accountSummary(acc account.Account, quota account.QuotaSnapshot) protocol.AccountSummary {
	summary := protocol.AccountSummary{
		ID:        acc.ID,
		Provider:  acc.Provider,
		AuthMode:  acc.AuthMode,
		Email:     acc.Email,
		AccountID: acc.AccountID,
		Plan:      acc.Plan,
	}
	if quota.AccountID != "" {
		summary.Quota = quotaSummary(quota)
	}
	if summary.Plan == "" && quota.AccountID != "" {
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
		QuotaState:            accountQuotaState(q),
	}
}

func accountQuotaState(q account.QuotaSnapshot) string {
	if account.QuotaSnapshotFresh(q, time.Now()) {
		return protocol.AccountQuotaStateFresh
	}
	if q.CheckedAt > 0 {
		return protocol.AccountQuotaStateStale
	}
	return protocol.AccountQuotaStateMissing
}
