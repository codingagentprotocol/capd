package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/internal/account/codexquota"
	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/repairplan"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

const codexAgentID = "codex"
const freshQuotaRefreshHint = "auto route does not have fresh cached quota; refresh quota first with accounts/quota accountId=\"all\" or accounts/check refreshQuota=true"

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
	if perr := rejectConcreteAccountID(accountID); perr != nil {
		return nil, perr
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
		s.recordSecretAccessError(err)
		return nil, protocol.NewError(protocol.CodeInternalError, "project account runtime: %v", err)
	}
	if len(profile.Env) == 0 {
		return nil, protocol.NewError(protocol.CodeInternalError, "%v", fmt.Errorf("empty runtime environment for account %q", accountID))
	}
	return profile.Env, nil
}

func rejectReservedAccountID(accountID string) *protocol.Error {
	if strings.TrimSpace(accountID) == protocol.AccountAll {
		return protocol.NewError(protocol.CodeInvalidParams, "accountId %q is reserved for accounts/quota batch refresh", protocol.AccountAll)
	}
	return nil
}

func rejectConcreteAccountID(accountID string) *protocol.Error {
	accountID = strings.TrimSpace(accountID)
	if perr := rejectReservedAccountID(accountID); perr != nil {
		return perr
	}
	if accountID == protocol.AccountAuto {
		return protocol.NewError(protocol.CodeInvalidParams, "accountId %q is supported only for account-aware routing", protocol.AccountAuto)
	}
	return nil
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
	provider := strings.TrimSpace(params.Provider)
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
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Provider != accounts[j].Provider {
			return accounts[i].Provider < accounts[j].Provider
		}
		return accounts[i].ID < accounts[j].ID
	})
	result := protocol.AccountsListResult{
		CurrentAccountID: current,
		Accounts:         make([]protocol.AccountSummary, 0, len(accounts)),
	}
	currentByProvider := map[string]string{}
	if provider != "" {
		currentByProvider[provider] = current
	}
	for _, acc := range accounts {
		accCurrent, ok := currentByProvider[acc.Provider]
		if !ok {
			accCurrent, err = s.opts.Accounts.CurrentAccount(acc.Provider)
			if err != nil {
				return protocol.AccountsListResult{}, protocol.NewError(protocol.CodeInternalError, "load current account: %v", err)
			}
			currentByProvider[acc.Provider] = accCurrent
		}
		summary := accountSummaryWithRoute(s.opts.Accounts, acc, account.QuotaSnapshot{}, accCurrent)
		if q, err := s.opts.Accounts.LoadQuota(acc.ID); err == nil {
			summary = accountSummaryWithRoute(s.opts.Accounts, acc, q, accCurrent)
		}
		result.Accounts = append(result.Accounts, summary)
	}
	return result, nil
}

func (s *Server) listProfiles(params protocol.ProfilesListParams) (protocol.ProfilesListResult, *protocol.Error) {
	if s.opts.Accounts == nil {
		return protocol.ProfilesListResult{Provider: codexauth.Provider, Profiles: []protocol.AccountProfileSummary{}}, nil
	}
	provider := profileProvider(params.Provider)
	name := strings.TrimSpace(params.Name)
	current, err := s.opts.Accounts.CurrentProfile(provider)
	if err != nil {
		return protocol.ProfilesListResult{}, protocol.NewError(protocol.CodeInternalError, "load current profile: %v", err)
	}
	if name != "" {
		profile, err := s.opts.Accounts.LoadProfile(provider, name)
		if err != nil {
			return protocol.ProfilesListResult{}, protocol.NewError(protocol.CodeInvalidParams, "unknown profile %q", name)
		}
		members, err := s.opts.Accounts.ProfileAccounts(provider, name)
		if err != nil {
			return protocol.ProfilesListResult{}, protocol.NewError(protocol.CodeInternalError, "load profile members: %v", err)
		}
		currentAccount, err := s.opts.Accounts.CurrentAccount(provider)
		if err != nil {
			return protocol.ProfilesListResult{}, protocol.NewError(protocol.CodeInternalError, "load current account: %v", err)
		}
		return protocol.ProfilesListResult{
			Provider:       provider,
			CurrentProfile: current,
			Profiles:       []protocol.AccountProfileSummary{profileSummary(profile, current, len(members))},
			Accounts:       accountSummariesWithQuota(s.opts.Accounts, members, currentAccount),
		}, nil
	}
	profiles, err := s.opts.Accounts.ListProfiles(provider)
	if err != nil {
		return protocol.ProfilesListResult{}, protocol.NewError(protocol.CodeInternalError, "list profiles: %v", err)
	}
	summaries := make([]protocol.AccountProfileSummary, 0, len(profiles))
	for _, profile := range profiles {
		members, err := s.opts.Accounts.ProfileAccounts(profile.Provider, profile.Name)
		if err != nil {
			return protocol.ProfilesListResult{}, protocol.NewError(protocol.CodeInternalError, "load profile members: %v", err)
		}
		summaries = append(summaries, profileSummary(profile, current, len(members)))
	}
	return protocol.ProfilesListResult{Provider: provider, CurrentProfile: current, Profiles: summaries}, nil
}

func (s *Server) updateProfile(params protocol.ProfilesUpdateParams) (protocol.ProfilesUpdateResult, *protocol.Error) {
	if s.opts.Accounts == nil {
		return protocol.ProfilesUpdateResult{}, protocol.NewError(protocol.CodeInvalidParams, "account support is not configured")
	}
	provider := profileProvider(params.Provider)
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return protocol.ProfilesUpdateResult{}, protocol.NewError(protocol.CodeInvalidParams, "profile name is required")
	}
	if params.Delete {
		if err := s.opts.Accounts.DeleteProfile(provider, name); err != nil {
			return protocol.ProfilesUpdateResult{}, protocol.NewError(protocol.CodeInvalidParams, "delete profile: %v", err)
		}
		current, err := s.opts.Accounts.CurrentProfile(provider)
		if err != nil {
			return protocol.ProfilesUpdateResult{}, protocol.NewError(protocol.CodeInternalError, "load current profile: %v", err)
		}
		return protocol.ProfilesUpdateResult{Provider: provider, CurrentProfile: current, Deleted: true}, nil
	}
	profile := account.Profile{Provider: provider, Name: name, Description: strings.TrimSpace(params.Description)}
	if err := s.opts.Accounts.UpsertProfile(profile); err != nil {
		return protocol.ProfilesUpdateResult{}, protocol.NewError(protocol.CodeInvalidParams, "save profile: %v", err)
	}
	if params.SetCurrent {
		if err := s.opts.Accounts.SetCurrentProfile(provider, name); err != nil {
			return protocol.ProfilesUpdateResult{}, protocol.NewError(protocol.CodeInvalidParams, "set current profile: %v", err)
		}
	}
	current, err := s.opts.Accounts.CurrentProfile(provider)
	if err != nil {
		return protocol.ProfilesUpdateResult{}, protocol.NewError(protocol.CodeInternalError, "load current profile: %v", err)
	}
	members, err := s.opts.Accounts.ProfileAccounts(provider, name)
	if err != nil {
		return protocol.ProfilesUpdateResult{}, protocol.NewError(protocol.CodeInternalError, "load profile members: %v", err)
	}
	summary := profileSummary(profile, current, len(members))
	return protocol.ProfilesUpdateResult{Provider: provider, CurrentProfile: current, Profile: &summary}, nil
}

func (s *Server) updateProfileMembers(params protocol.ProfilesMembersParams) (protocol.ProfilesMembersResult, *protocol.Error) {
	if s.opts.Accounts == nil {
		return protocol.ProfilesMembersResult{}, protocol.NewError(protocol.CodeInvalidParams, "account support is not configured")
	}
	provider := profileProvider(params.Provider)
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return protocol.ProfilesMembersResult{}, protocol.NewError(protocol.CodeInvalidParams, "profile name is required")
	}
	if len(params.AddAccountIDs) == 0 && len(params.RemoveAccountIDs) == 0 {
		return protocol.ProfilesMembersResult{}, protocol.NewError(protocol.CodeInvalidParams, "addAccountIds or removeAccountIds is required")
	}
	for _, id := range params.AddAccountIDs {
		accountID := strings.TrimSpace(id)
		if accountID == "" {
			continue
		}
		if err := s.opts.Accounts.AddProfileAccount(provider, name, accountID); err != nil {
			return protocol.ProfilesMembersResult{}, protocol.NewError(protocol.CodeInvalidParams, "add profile member %q: %v", accountID, err)
		}
	}
	for _, id := range params.RemoveAccountIDs {
		accountID := strings.TrimSpace(id)
		if accountID == "" {
			continue
		}
		if err := s.opts.Accounts.RemoveProfileAccount(provider, name, accountID); err != nil {
			return protocol.ProfilesMembersResult{}, protocol.NewError(protocol.CodeInvalidParams, "remove profile member %q: %v", accountID, err)
		}
	}
	profile, err := s.opts.Accounts.LoadProfile(provider, name)
	if err != nil {
		return protocol.ProfilesMembersResult{}, protocol.NewError(protocol.CodeInvalidParams, "unknown profile %q", name)
	}
	currentProfile, err := s.opts.Accounts.CurrentProfile(provider)
	if err != nil {
		return protocol.ProfilesMembersResult{}, protocol.NewError(protocol.CodeInternalError, "load current profile: %v", err)
	}
	currentAccount, err := s.opts.Accounts.CurrentAccount(provider)
	if err != nil {
		return protocol.ProfilesMembersResult{}, protocol.NewError(protocol.CodeInternalError, "load current account: %v", err)
	}
	members, err := s.opts.Accounts.ProfileAccounts(provider, name)
	if err != nil {
		return protocol.ProfilesMembersResult{}, protocol.NewError(protocol.CodeInternalError, "load profile members: %v", err)
	}
	return protocol.ProfilesMembersResult{
		Provider:       provider,
		CurrentProfile: currentProfile,
		Profile:        profileSummary(profile, currentProfile, len(members)),
		Accounts:       accountSummariesWithQuota(s.opts.Accounts, members, currentAccount),
	}, nil
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
	authPaths := cleanImportAuthPaths(params.AuthPaths)
	if len(authPaths) == 0 {
		authPath := strings.TrimSpace(params.AuthPath)
		if authPath != "" {
			authPaths = append(authPaths, authPath)
		}
	}
	if len(authPaths) == 0 {
		var err error
		authPath, err := codexauth.DefaultAuthPath("")
		if err != nil {
			return protocol.AccountsImportResult{}, protocol.NewError(protocol.CodeInternalError, "default auth path: %v", err)
		}
		authPaths = append(authPaths, authPath)
	}
	importer := codexauth.Importer{Accounts: s.opts.Accounts, Secrets: s.opts.Secrets}
	imported := make([]protocol.AccountSummary, 0, len(authPaths))
	for _, authPath := range authPaths {
		result, err := importer.ImportAuthJSON(ctx, authPath)
		if err != nil {
			return protocol.AccountsImportResult{}, protocol.NewError(protocol.CodeInvalidParams, "import account: %s", codexauth.SafeImportError(err, authPath))
		}
		imported = append(imported, accountSummary(result.Account, account.QuotaSnapshot{}))
	}
	current, err := s.opts.Accounts.CurrentAccount(provider)
	if err != nil {
		return protocol.AccountsImportResult{}, protocol.NewError(protocol.CodeInternalError, "load current account: %v", err)
	}
	list, err := s.opts.Accounts.ListAccounts(provider)
	if err != nil {
		return protocol.AccountsImportResult{}, protocol.NewError(protocol.CodeInternalError, "list imported accounts: %v", err)
	}
	var last protocol.AccountSummary
	if len(imported) > 0 {
		last = imported[len(imported)-1]
	}
	return protocol.AccountsImportResult{
		CurrentAccountID: current,
		ImportedAccounts: len(list),
		Account:          last,
		Accounts:         imported,
	}, nil
}

func cleanImportAuthPaths(paths []string) []string {
	clean := make([]string, 0, len(paths))
	for _, raw := range paths {
		if path := strings.TrimSpace(raw); path != "" {
			clean = append(clean, path)
		}
	}
	return clean
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
		if perr := rejectConcreteAccountID(accountID); perr != nil {
			return protocol.AccountsCurrentResult{}, perr
		}
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
	requiredBackend, err := secret.NormalizeBackend(params.RequireSecretBackend)
	if err != nil {
		return protocol.AccountsCheckResult{}, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
	}
	params.RequireSecretBackend = requiredBackend
	current, err := s.opts.Accounts.CurrentAccount(provider)
	if err != nil {
		return protocol.AccountsCheckResult{}, protocol.NewError(protocol.CodeInternalError, "load current account: %v", err)
	}
	accounts, err := s.opts.Accounts.ListAccounts(provider)
	if err != nil {
		return protocol.AccountsCheckResult{}, protocol.NewError(protocol.CodeInternalError, "list accounts: %v", err)
	}
	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].ID < accounts[j].ID
	})
	result := protocol.AccountsCheckResult{
		Provider:         provider,
		CurrentAccountID: current,
		SecretBackend:    s.opts.Secrets.Backend(),
		CheckedAccounts:  len(accounts),
		QuotaRefreshed:   params.RefreshQuota,
		Accounts:         make([]protocol.AccountCheckEvidence, 0, len(accounts)),
	}
	if len(accounts) > 0 {
		result = s.withCachedAccountsCheckEvidence(result, accounts, current, provider)
	}
	if perr := validateAccountsCheckPreflight(s.opts.Secrets.Backend(), len(accounts), params); perr != nil {
		return protocol.AccountsCheckResult{}, accountsCheckErrorWithEvidence(perr, result, params)
	}
	if params.RefreshQuota {
		if _, perr := s.refreshAccountQuota(ctx, protocol.AccountsQuotaParams{Provider: provider, AccountID: protocol.AccountAll}); perr != nil {
			if refreshedAccounts, err := s.opts.Accounts.ListAccounts(provider); err == nil {
				sort.Slice(refreshedAccounts, func(i, j int) bool {
					return refreshedAccounts[i].ID < refreshedAccounts[j].ID
				})
				accounts = refreshedAccounts
				result.CheckedAccounts = len(accounts)
				result.Accounts = nil
				result.AutoRoute = nil
				result.RouteCandidates = nil
				result = s.withCachedAccountsCheckEvidence(result, accounts, current, provider)
			}
			return protocol.AccountsCheckResult{}, accountsCheckErrorWithEvidence(protocol.NewError(perr.Code, "refresh quota: %s", perr.Message), result, params)
		}
		accounts, err = s.opts.Accounts.ListAccounts(provider)
		if err != nil {
			return protocol.AccountsCheckResult{}, protocol.NewError(protocol.CodeInternalError, "list refreshed accounts: %v", err)
		}
		sort.Slice(accounts, func(i, j int) bool {
			return accounts[i].ID < accounts[j].ID
		})
		result.CheckedAccounts = len(accounts)
		result.Accounts = make([]protocol.AccountCheckEvidence, 0, len(accounts))
		result.AutoRoute = nil
		result.RouteCandidates = nil
		result = s.withCachedAccountsCheckEvidence(result, accounts, current, provider)
	}
	result.Accounts = result.Accounts[:0]
	for _, acc := range accounts {
		row, perr := s.checkAccount(ctx, acc, current)
		if row.ID != "" {
			result.Accounts = append(result.Accounts, row)
		}
		if perr != nil {
			return protocol.AccountsCheckResult{}, accountsCheckErrorWithEvidence(perr, result, params)
		}
	}
	if len(accounts) > 0 {
		selected, _, perr := s.selectCodexAccountForRoute("")
		if perr != nil {
			return protocol.AccountsCheckResult{}, accountsCheckErrorWithEvidence(perr, result, params)
		}
		evidence := account.QuotaRouteEvidence(s.opts.Accounts, selected)
		result.AutoRoute = &evidence
		if candidates, err := account.QuotaRouteCandidates(s.opts.Accounts, provider); err == nil {
			result.RouteCandidates = candidates
		}
	}
	if perr := validateAccountsCheckResult(result, params); perr != nil {
		return protocol.AccountsCheckResult{}, accountsCheckErrorWithEvidence(perr, result, params)
	}
	result = accountsCheckWithSummaryAndRepairPlan(result, params)
	return result, nil
}

func (s *Server) withCachedAccountsCheckEvidence(result protocol.AccountsCheckResult, accounts []account.Account, current, provider string) protocol.AccountsCheckResult {
	if len(accounts) == 0 || s.opts.Accounts == nil {
		return result
	}
	if len(result.Accounts) == 0 {
		result.Accounts = make([]protocol.AccountCheckEvidence, 0, len(accounts))
		for _, acc := range accounts {
			result.Accounts = append(result.Accounts, s.baseAccountCheckEvidence(acc, current))
		}
	}
	if result.AutoRoute == nil {
		if selected, err := account.SelectQuotaRouteAccount(s.opts.Accounts, provider); err == nil {
			evidence := account.QuotaRouteEvidence(s.opts.Accounts, selected)
			result.AutoRoute = &evidence
		}
	}
	if len(result.RouteCandidates) == 0 {
		if candidates, err := account.QuotaRouteCandidates(s.opts.Accounts, provider); err == nil {
			result.RouteCandidates = candidates
		}
	}
	return result
}

func (s *Server) cachedAccountsCheck(provider string) (protocol.AccountsCheckResult, *protocol.Error) {
	if s.opts.Accounts == nil || s.opts.Secrets == nil {
		return protocol.AccountsCheckResult{}, protocol.NewError(protocol.CodeInvalidParams, "account support is not configured")
	}
	if provider == "" {
		provider = codexauth.Provider
	}
	current, err := s.opts.Accounts.CurrentAccount(provider)
	if err != nil {
		return protocol.AccountsCheckResult{}, protocol.NewError(protocol.CodeInternalError, "load current account: %v", err)
	}
	accounts, err := s.opts.Accounts.ListAccounts(provider)
	if err != nil {
		return protocol.AccountsCheckResult{}, protocol.NewError(protocol.CodeInternalError, "list accounts: %v", err)
	}
	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].ID < accounts[j].ID
	})
	result := protocol.AccountsCheckResult{
		Provider:         provider,
		CurrentAccountID: current,
		SecretBackend:    s.opts.Secrets.Backend(),
		CheckedAccounts:  len(accounts),
		Accounts:         make([]protocol.AccountCheckEvidence, 0, len(accounts)),
	}
	if len(accounts) > 0 {
		result = s.withCachedAccountsCheckEvidence(result, accounts, current, provider)
	}
	result = accountsCheckWithSummaryAndRepairPlan(result, protocol.AccountsCheckParams{Provider: provider})
	return result, nil
}

func accountsCheckErrorWithEvidence(perr *protocol.Error, result protocol.AccountsCheckResult, params protocol.AccountsCheckParams) *protocol.Error {
	if perr == nil {
		return nil
	}
	result = accountsCheckWithSummaryAndRepairPlan(result, params)
	perr.Data = result
	return perr
}

func accountsCheckWithSummaryAndRepairPlan(result protocol.AccountsCheckResult, params protocol.AccountsCheckParams) protocol.AccountsCheckResult {
	if result.AutoRoute != nil || len(result.RouteCandidates) > 0 {
		policy := account.DefaultRoutePolicyEvidence()
		result.RoutePolicy = &policy
	}
	result.Summary = accountsCheckSummary(result, params)
	result.RepairPlan = accountsCheckRepairPlan(result, params)
	return result
}

func accountsCheckSummary(result protocol.AccountsCheckResult, params protocol.AccountsCheckParams) protocol.AccountsCheckSummary {
	requiredAccounts := 2
	missingAccounts := requiredAccounts - result.CheckedAccounts
	if missingAccounts < 0 {
		missingAccounts = 0
	}
	summary := protocol.AccountsCheckSummary{
		CheckedAccounts:       result.CheckedAccounts,
		RequiredAccounts:      requiredAccounts,
		MissingAccounts:       missingAccounts,
		RouteCandidates:       len(result.RouteCandidates),
		SecretBackend:         result.SecretBackend,
		RequiredSecretBackend: params.RequireSecretBackend,
		SecretBackendOK:       params.RequireSecretBackend == "" || result.SecretBackend == params.RequireSecretBackend,
		QuotaRefreshed:        result.QuotaRefreshed,
	}
	for _, row := range result.Accounts {
		switch row.QuotaState {
		case protocol.AccountQuotaStateFresh:
			summary.FreshQuotaAccounts++
		case protocol.AccountQuotaStateStale:
			summary.StaleQuotaAccounts++
		default:
			summary.MissingQuotaAccounts++
		}
	}
	if result.AutoRoute != nil {
		summary.AutoRouteAccountID = result.AutoRoute.AccountID
		summary.AutoRouteFresh = result.AutoRoute.Fresh
		summary.RouteDecisionOK = true
	}
	accountsReady := result.CheckedAccounts > 0 && len(result.Accounts) == result.CheckedAccounts
	for _, row := range result.Accounts {
		accountsReady = accountsReady && row.SecretBackendOK && row.CredentialReadable && row.RuntimeReady
	}
	summary.Ready = result.CheckedAccounts > 0 && summary.SecretBackendOK && accountsReady
	if params.RequireMultiple {
		summary.Ready = summary.Ready && result.CheckedAccounts >= requiredAccounts
	}
	if params.RequireFreshQuota {
		summary.Ready = summary.Ready && summary.AutoRouteFresh
	}
	if params.RequireAllFreshQuota {
		summary.Ready = summary.Ready && result.CheckedAccounts > 0 && summary.FreshQuotaAccounts == result.CheckedAccounts
	}
	return summary
}

func accountsCheckRepairPlan(result protocol.AccountsCheckResult, params protocol.AccountsCheckParams) []protocol.RepairStep {
	if result.Summary.Ready {
		return nil
	}
	backend := params.RequireSecretBackend
	if backend == "" {
		backend = result.SecretBackend
	}
	steps := []protocol.RepairStep{}
	if params.RequireSecretBackend != "" && !result.Summary.SecretBackendOK {
		steps = append(steps, protocol.RepairStep{
			ID:               "restart-daemon-secret-backend",
			Title:            "Restart capd with the required SecretStore backend",
			Command:          "capd start --secret-backend " + params.RequireSecretBackend,
			ExpectedEvidence: "accounts/check summary shows secretBackendOk=true",
			RequiresSecret:   true,
		})
	}
	if result.Summary.MissingAccounts > 0 {
		steps = append(steps, protocol.RepairStep{
			ID:               "import-codex-accounts",
			Title:            "Import enough Codex accounts through CAP",
			Command:          "capd accounts import --auth /path/a/auth.json --auth /path/b/auth.json",
			ExpectedEvidence: "accounts/check summary shows checkedAccounts>=2 and missingAccounts=0",
			RequiresDaemon:   true,
			RequiresSecret:   true,
		})
	}
	if accountsCheckHasUnreadableCredentials(result) {
		steps = append(steps, protocol.RepairStep{
			ID:               "repair-secretstore",
			Title:            "Repair unreadable Codex account credentials",
			Command:          accountsCheckSecretStoreCommand(backend),
			ExpectedEvidence: "accounts/check account credentials are readable",
			RequiresSecret:   true,
		})
	}
	if result.CheckedAccounts > 0 && (result.Summary.FreshQuotaAccounts < result.CheckedAccounts || !result.Summary.AutoRouteFresh || params.RequireFreshQuota || params.RequireAllFreshQuota) {
		steps = append(steps, protocol.RepairStep{
			ID:               "refresh-quota-readiness",
			Title:            "Refresh quota and verify daemon-side readiness",
			Command:          accountsCheckReadinessRepairCommand(backend),
			ExpectedEvidence: "accounts/check summary shows ready=true, quotaRefreshed=true, autoRouteFresh=true",
			RequiresDaemon:   true,
			RequiresSecret:   true,
		})
	}
	if params.RequireFreshQuota && !result.Summary.AutoRouteFresh {
		steps = append(steps, protocol.RepairStep{
			ID:               "preview-auto-route",
			Title:            "Preview fresh quota-aware Codex routing",
			Command:          "capd agents route --account auto --require-fresh-quota --json",
			ExpectedEvidence: "route decision returns ok=true with a fresh accountRoute",
			RequiresDaemon:   true,
		})
	}
	if len(steps) > 0 {
		steps = append(steps, protocol.RepairStep{
			ID:               "final-live-preflight",
			Title:            "Run the full live Codex preflight",
			Command:          "make live-codex-preflight",
			ExpectedEvidence: "preflight exits 0 and routeCandidates show a fresh auto route",
			RequiresDaemon:   true,
			RequiresSecret:   true,
		})
	}
	return repairplan.Annotate(steps, repairplan.Options{})
}

func accountsCheckHasUnreadableCredentials(result protocol.AccountsCheckResult) bool {
	for _, row := range result.Accounts {
		if !row.SecretBackendOK || !row.CredentialReadable {
			return true
		}
	}
	return false
}

func accountsCheckSecretStoreCommand(backend string) string {
	cmd := "capd secretstore check --json --roundtrip"
	if backend != "" {
		cmd += " --secret-backend " + backend + " --require-backend " + backend
	}
	return cmd + " --timeout 2m"
}

func accountsCheckReadinessRepairCommand(backend string) string {
	cmd := "capd accounts check --json --readiness"
	if backend != "" {
		cmd += " --require-secret-backend " + backend
	}
	return cmd + " --timeout 2m"
}

func validateAccountsCheckPreflight(secretBackend string, checkedAccounts int, params protocol.AccountsCheckParams) *protocol.Error {
	if params.RequireSecretBackend != "" && secretBackend != params.RequireSecretBackend {
		return protocol.NewError(protocol.CodeInvalidParams, "secret backend = %q, want %q", secretBackend, params.RequireSecretBackend)
	}
	if params.RefreshQuota && checkedAccounts == 0 {
		return protocol.NewError(protocol.CodeInvalidParams, "no imported Codex accounts")
	}
	return nil
}

func validateAccountsCheckResult(result protocol.AccountsCheckResult, params protocol.AccountsCheckParams) *protocol.Error {
	if params.RequireSecretBackend != "" && result.SecretBackend != params.RequireSecretBackend {
		return protocol.NewError(protocol.CodeInvalidParams, "secret backend = %q, want %q", result.SecretBackend, params.RequireSecretBackend)
	}
	if params.RequireMultiple && result.CheckedAccounts < 2 {
		return protocol.NewError(protocol.CodeInvalidParams, "expected multiple Codex accounts, found %d", result.CheckedAccounts)
	}
	if (params.RequireFreshQuota || params.RequireAllFreshQuota) && result.CheckedAccounts == 0 {
		return protocol.NewError(protocol.CodeInvalidParams, "no Codex accounts checked; import accounts first")
	}
	if params.RequireFreshQuota && (result.AutoRoute == nil || !result.AutoRoute.Fresh) {
		return protocol.NewError(protocol.CodeInvalidParams, freshQuotaRefreshHint)
	}
	if params.RequireAllFreshQuota {
		var stale []string
		for _, row := range result.Accounts {
			if !row.QuotaFresh {
				stale = append(stale, fmt.Sprintf("%s=%s", row.ID, row.QuotaState))
			}
		}
		if len(stale) > 0 {
			return protocol.NewError(protocol.CodeInvalidParams, "quota is not fresh for %s; refresh every account first", strings.Join(stale, ", "))
		}
	}
	return nil
}

func (s *Server) checkAccount(ctx context.Context, acc account.Account, current string) (protocol.AccountCheckEvidence, *protocol.Error) {
	row := s.baseAccountCheckEvidence(acc, current)
	ref, err := secret.ParseRef(acc.SecretRef)
	if err != nil {
		row.SecretState = protocol.AccountSecretStateMalformedRef
		return row, protocol.NewError(protocol.CodeInternalError, "parse account secret ref: %s", row.SecretState)
	}
	if err := secret.EnsureRefBackend(s.opts.Secrets, ref); err != nil {
		row.SecretState = protocol.AccountSecretStateBackendMismatch
		return row, protocol.NewError(protocol.CodeInternalError, "account secret backend mismatch")
	}
	if _, err := s.opts.Secrets.Get(ctx, ref); err != nil {
		row.SecretBackendOK = true
		row.SecretState = accountSecretErrorState(err)
		s.recordSecretState(row.SecretState)
		return row, protocol.NewError(protocol.CodeInternalError, "load account credentials: %s", row.SecretState)
	}
	row.SecretBackendOK = true
	row.SecretState = protocol.AccountSecretStateReadable
	row.CredentialReadable = true
	env, perr := s.runtimeEnvForAccount(ctx, codexAgentID, acc.ID)
	if perr != nil {
		return row, perr
	}
	codexHome := codexHomeFromEnv(env)
	if codexHome == "" {
		return row, protocol.NewError(protocol.CodeInternalError, "project account runtime: CODEX_HOME missing")
	}
	evidence, err := codexauth.VerifyRuntimeProfile(codexauth.RuntimeProfile{
		AccountID: acc.ID,
		CodexHome: codexHome,
		Env:       env,
	})
	if err != nil {
		return row, protocol.NewError(protocol.CodeInternalError, "verify account runtime: %v", err)
	}
	row.RuntimeReady = evidence.RuntimeEnvOK && evidence.AuthJSONPrivate && evidence.ProjectionMarkerOK
	row.AuthJSONPrivate = evidence.AuthJSONPrivate
	row.ProjectionMarkerOK = evidence.ProjectionMarkerOK
	return row, nil
}

func (s *Server) baseAccountCheckEvidence(acc account.Account, current string) protocol.AccountCheckEvidence {
	row := protocol.AccountCheckEvidence{
		ID:         acc.ID,
		Email:      acc.Email,
		Current:    acc.ID == current,
		QuotaState: protocol.AccountQuotaStateMissing,
	}
	if q, err := s.opts.Accounts.LoadQuota(acc.ID); err == nil {
		row.QuotaState = accountQuotaState(q)
		row.QuotaCheckedAt = q.CheckedAt
		row.PrimaryUsedPercent = &q.PrimaryUsedPercent
		row.QuotaFresh = row.QuotaState == protocol.AccountQuotaStateFresh
	}
	return row
}

func accountSecretErrorState(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return protocol.AccountSecretStateTimeout
	case os.IsNotExist(err):
		return protocol.AccountSecretStateMissing
	case strings.Contains(err.Error(), "macOS keychain status -25300"):
		return protocol.AccountSecretStateMissing
	case strings.Contains(err.Error(), "macOS keychain status -128"):
		return protocol.AccountSecretStateAccessDenied
	default:
		return protocol.AccountSecretStateUnreadable
	}
}

func (s *Server) recordSecretAccessError(err error) {
	s.recordSecretState(accountSecretErrorState(err))
}

func (s *Server) recordSecretState(state string) {
	if state == protocol.AccountSecretStateAccessDenied {
		s.metrics.recordSecretAccessDenied()
	}
}

func (s *Server) refreshAccountQuota(ctx context.Context, params protocol.AccountsQuotaParams) (protocol.AccountsQuotaResult, *protocol.Error) {
	if s.opts.Accounts == nil || s.opts.Secrets == nil {
		return protocol.AccountsQuotaResult{}, protocol.NewError(protocol.CodeInvalidParams, "account support is not configured")
	}
	provider := strings.TrimSpace(params.Provider)
	if provider == "" {
		provider = codexauth.Provider
	}
	if provider != codexauth.Provider {
		return protocol.AccountsQuotaResult{}, protocol.NewError(protocol.CodeInvalidParams, "quota refresh is currently supported only for provider %q", codexauth.Provider)
	}
	accountID := strings.TrimSpace(params.AccountID)
	if accountID == protocol.AccountAll {
		accounts, err := s.opts.Accounts.ListAccounts(provider)
		if err != nil {
			return protocol.AccountsQuotaResult{}, protocol.NewError(protocol.CodeInternalError, "list accounts: %v", err)
		}
		if len(accounts) == 0 {
			return protocol.AccountsQuotaResult{}, protocol.NewError(protocol.CodeInvalidParams, "no imported Codex accounts")
		}
		sort.Slice(accounts, func(i, j int) bool {
			return accounts[i].ID < accounts[j].ID
		})
		result := protocol.AccountsQuotaResult{
			Accounts: make([]protocol.AccountSummary, 0, len(accounts)),
		}
		for _, acc := range accounts {
			summary, perr := s.refreshOneAccountQuota(ctx, acc)
			if perr != nil {
				return protocol.AccountsQuotaResult{}, s.accountsQuotaAllErrorWithEvidence(perr, acc.ID, result)
			}
			result.Accounts = append(result.Accounts, summary)
		}
		return result, nil
	}
	var err error
	if accountID == "" {
		accountID, err = s.opts.Accounts.CurrentAccount(provider)
		if err != nil {
			return protocol.AccountsQuotaResult{}, protocol.NewError(protocol.CodeInternalError, "load current account: %v", err)
		}
	} else if accountID == protocol.AccountAuto {
		acc, _, perr := s.selectCodexAccountForRoute("")
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
	summary, perr := s.refreshOneAccountQuota(ctx, acc)
	if perr != nil {
		return protocol.AccountsQuotaResult{}, perr
	}
	return protocol.AccountsQuotaResult{Account: summary}, nil
}

func (s *Server) accountsQuotaAllErrorWithEvidence(perr *protocol.Error, accountID string, partial protocol.AccountsQuotaResult) *protocol.Error {
	if perr == nil {
		return nil
	}
	out := protocol.NewError(perr.Code, "%s: %s", accountID, perr.Message)
	partial.FailedAccount = accountID
	partial.NextSteps = accountsQuotaAllNextSteps(s.secretBackend())
	if len(partial.Accounts) > 0 || partial.FailedAccount != "" {
		out.Data = partial
	}
	return out
}

func accountsQuotaAllNextSteps(backend string) []string {
	readiness := "capd accounts check --json --readiness"
	if backend != "" {
		readiness += " --require-secret-backend " + backend
	}
	readiness += " --timeout 2m"
	return []string{
		"inspect safe daemon account evidence with: " + readiness,
		"after fixing the failing account, rerun quota refresh with: " + readiness,
	}
}

func (s *Server) secretBackend() string {
	if s == nil || s.opts.Secrets == nil {
		return ""
	}
	return s.opts.Secrets.Backend()
}

func (s *Server) refreshOneAccountQuota(ctx context.Context, acc account.Account) (protocol.AccountSummary, *protocol.Error) {
	ref, err := secret.ParseRef(acc.SecretRef)
	if err != nil {
		return protocol.AccountSummary{}, protocol.NewError(protocol.CodeInternalError, "parse secret ref: %v", err)
	}
	if err := secret.EnsureRefBackend(s.opts.Secrets, ref); err != nil {
		return protocol.AccountSummary{}, protocol.NewError(protocol.CodeInternalError, "%v", err)
	}
	bundle, err := s.opts.Secrets.Get(ctx, ref)
	if err != nil {
		s.recordSecretAccessError(err)
		return protocol.AccountSummary{}, protocol.NewError(protocol.CodeInternalError, "load account secret: %v", err)
	}
	if bundle.AccountID == "" {
		bundle.AccountID = acc.AccountID
	}
	updatedAcc, changed := codexauth.AccountWithBundleMetadata(acc, bundle)
	result, err := codexquota.Client{BaseURL: s.opts.CodexQuotaBaseURL}.Usage(ctx, acc.ID, bundle)
	if err != nil {
		return protocol.AccountSummary{}, protocol.NewError(protocol.CodeAgentUnavailable, "quota: %v", err)
	}
	if updatedAcc.Plan == "" && result.Quota.Plan != "" {
		updatedAcc.Plan = result.Quota.Plan
		changed = true
	}
	if changed {
		if err := s.opts.Accounts.UpsertAccount(updatedAcc); err != nil {
			return protocol.AccountSummary{}, protocol.NewError(protocol.CodeInternalError, "update account metadata: %v", err)
		}
		acc = updatedAcc
	}
	if err := s.opts.Accounts.SaveQuota(result.Quota); err != nil {
		return protocol.AccountSummary{}, protocol.NewError(protocol.CodeInternalError, "save quota: %v", err)
	}
	return accountSummary(acc, result.Quota), nil
}

func (s *Server) saveUsageQuota(ctx context.Context, accountID string, usage map[string]any) *protocol.Error {
	if s.opts.Accounts == nil {
		return nil
	}
	quota := account.QuotaFromUsage(accountID, usage)
	acc, err := s.opts.Accounts.LoadAccount(accountID)
	if err != nil {
		if err := s.opts.Accounts.SaveQuota(quota); err != nil {
			return protocol.NewError(protocol.CodeInternalError, "save usage quota: %v", err)
		}
		return nil
	}
	updatedAcc := acc
	changed := false
	if s.opts.Secrets != nil && acc.SecretRef != "" {
		ref, err := secret.ParseRef(acc.SecretRef)
		if err != nil {
			return protocol.NewError(protocol.CodeInternalError, "parse secret ref: %v", err)
		}
		if err := secret.EnsureRefBackend(s.opts.Secrets, ref); err != nil {
			return protocol.NewError(protocol.CodeInternalError, "%v", err)
		}
		bundle, err := s.opts.Secrets.Get(ctx, ref)
		if err != nil {
			s.recordSecretAccessError(err)
			return protocol.NewError(protocol.CodeInternalError, "load account secret: %v", err)
		}
		updatedAcc, changed = codexauth.AccountWithBundleMetadata(acc, bundle)
	}
	if updatedAcc.Plan == "" && quota.Plan != "" {
		updatedAcc.Plan = quota.Plan
		changed = true
	}
	if changed {
		if err := s.opts.Accounts.UpsertAccount(updatedAcc); err != nil {
			return protocol.NewError(protocol.CodeInternalError, "update account metadata: %v", err)
		}
	}
	if err := s.opts.Accounts.SaveQuota(quota); err != nil {
		return protocol.NewError(protocol.CodeInternalError, "save usage quota: %v", err)
	}
	return nil
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
	if perr := rejectConcreteAccountID(accountID); perr != nil {
		return protocol.AccountsRemoveResult{}, perr
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
		s.recordSecretAccessError(err)
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
	return accountSummaryWithRoute(nil, acc, quota, "")
}

func profileProvider(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return codexauth.Provider
	}
	return provider
}

func profileSummary(profile account.Profile, current string, members int) protocol.AccountProfileSummary {
	return protocol.AccountProfileSummary{
		Current:     profile.Name == current,
		Provider:    profile.Provider,
		Name:        profile.Name,
		Description: profile.Description,
		Accounts:    members,
	}
}

func accountSummariesWithQuota(accounts *account.Store, list []account.Account, current string) []protocol.AccountSummary {
	summaries := make([]protocol.AccountSummary, 0, len(list))
	for _, acc := range list {
		var quota account.QuotaSnapshot
		if q, err := accounts.LoadQuota(acc.ID); err == nil {
			quota = q
		}
		summaries = append(summaries, accountSummaryWithRoute(accounts, acc, quota, current))
	}
	return summaries
}

func accountSummaryWithRoute(accounts *account.Store, acc account.Account, quota account.QuotaSnapshot, current string) protocol.AccountSummary {
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
		summary.QuotaFresh = summary.Quota.QuotaState == protocol.AccountQuotaStateFresh
	}
	if summary.Plan == "" && quota.AccountID != "" {
		summary.Plan = quota.Plan
	}
	if accounts != nil && acc.Provider == codexauth.Provider {
		score := account.QuotaRouteScore(accounts, acc, current)
		summary.RouteScore = &score
		summary.RouteReason = account.QuotaRouteReason(accounts, acc)
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
