package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/repairplan"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

//go:embed console_index.html
var consoleHTML string

//go:embed probe_index.html
var probeHTML string

const (
	probeDataDefaultTimeout   = 12 * time.Second
	probeDataReadinessTimeout = 2 * time.Minute
)

func (s *Server) handleConsole(w http.ResponseWriter, _ *http.Request) {
	writeLocalPageHeaders(w)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(consoleHTML))
}

func (s *Server) handleProbe(w http.ResponseWriter, _ *http.Request) {
	writeLocalPageHeaders(w)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(probeHTML))
}

type probeDataResult struct {
	OK              bool                            `json:"ok"`
	PromptFree      bool                            `json:"promptFree,omitempty"`
	Summary         probeDataSummary                `json:"summary"`
	Health          map[string]any                  `json:"health"`
	AccountsCheck   *protocol.AccountsCheckResult   `json:"accountsCheck,omitempty"`
	RouteDecision   *protocol.AgentRouteResult      `json:"routeDecision,omitempty"`
	AutoRoute       *protocol.AccountRouteEvidence  `json:"autoRoute,omitempty"`
	RouteCandidates []protocol.AccountRouteEvidence `json:"routeCandidates,omitempty"`
	Checks          []probeDataCheck                `json:"checks"`
	NextSteps       []string                        `json:"nextSteps,omitempty"`
	RepairPlan      []protocol.RepairStep           `json:"repairPlan,omitempty"`
	Errors          []probeDataError                `json:"errors,omitempty"`
}

type probeDataSummary struct {
	Ready                 bool   `json:"ready"`
	Readiness             bool   `json:"readiness"`
	CheckedAccounts       int    `json:"checkedAccounts"`
	RequiredAccounts      int    `json:"requiredAccounts"`
	MissingAccounts       int    `json:"missingAccounts"`
	FreshQuotaAccounts    int    `json:"freshQuotaAccounts"`
	StaleQuotaAccounts    int    `json:"staleQuotaAccounts"`
	MissingQuotaAccounts  int    `json:"missingQuotaAccounts"`
	AutoRouteAccountID    string `json:"autoRouteAccountId,omitempty"`
	AutoRouteFresh        bool   `json:"autoRouteFresh"`
	RouteDecisionOK       bool   `json:"routeDecisionOk"`
	RouteCandidates       int    `json:"routeCandidates"`
	SecretBackend         string `json:"secretBackend,omitempty"`
	RequiredSecretBackend string `json:"requiredSecretBackend,omitempty"`
	SecretBackendOK       bool   `json:"secretBackendOk"`
	QuotaRefreshed        bool   `json:"quotaRefreshed,omitempty"`
}

type probeDataCheck struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Evidence string `json:"evidence"`
	NextStep string `json:"nextStep,omitempty"`
}

type probeDataError struct {
	Source  string `json:"source"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handleProbeData(w http.ResponseWriter, r *http.Request) {
	writeLocalJSONHeaders(w)
	if !s.authorizedBearer(r) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "unauthorized",
		})
		return
	}
	readiness := parseBoolQuery(r, "readiness")
	requireSecretBackend := strings.TrimSpace(r.URL.Query().Get("requireSecretBackend"))
	if readiness && requireSecretBackend == "" {
		requireSecretBackend = secret.BackendNative
	}
	var err error
	requireSecretBackend, err = secret.NormalizeBackend(requireSecretBackend)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), probeDataTimeout(readiness))
	defer cancel()
	result := s.probeData(ctx, readiness, requireSecretBackend)
	status := http.StatusOK
	if !result.OK && readiness {
		status = http.StatusFailedDependency
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}

func probeDataTimeout(readiness bool) time.Duration {
	if readiness {
		return probeDataReadinessTimeout
	}
	return probeDataDefaultTimeout
}

func (s *Server) probeData(ctx context.Context, readiness bool, requireSecretBackend string) probeDataResult {
	secretBackend := ""
	if s.opts.Secrets != nil {
		secretBackend = s.opts.Secrets.Backend()
	}
	result := probeDataResult{
		Health: map[string]any{
			"ok":              true,
			"daemon":          "capd",
			"version":         s.opts.Version,
			"protocolVersion": protocol.Version,
			"secretBackend":   secretBackend,
		},
	}
	params := protocol.AccountsCheckParams{Provider: codexAgentID}
	if !readiness {
		result.PromptFree = true
		accountsCheck, perr := s.cachedAccountsCheck(codexAgentID)
		if perr != nil {
			result.Errors = append(result.Errors, probeError("accounts/list", perr))
		} else if accountsCheck.Provider != "" {
			result.AccountsCheck = &accountsCheck
			result.AutoRoute = accountsCheck.AutoRoute
			result.RouteCandidates = accountsCheck.RouteCandidates
		}
	} else {
		params.RefreshQuota = true
		params.RequireMultiple = true
		params.RequireFreshQuota = true
		params.RequireAllFreshQuota = true
		params.RequireSecretBackend = requireSecretBackend
		accountsCheck, perr := s.checkAccounts(ctx, params)
		if perr != nil {
			result.Errors = append(result.Errors, probeError("accounts/check", perr))
			if partial, ok := perr.Data.(protocol.AccountsCheckResult); ok {
				accountsCheck = partial
			}
		}
		if accountsCheck.Provider != "" {
			result.AccountsCheck = &accountsCheck
			result.AutoRoute = accountsCheck.AutoRoute
			result.RouteCandidates = accountsCheck.RouteCandidates
		}
	}
	route, perr := s.routeAgent(ctx, protocol.AgentRouteParams{
		AccountID:         protocol.AccountAuto,
		RequireFreshQuota: readiness,
	})
	if perr != nil {
		result.Errors = append(result.Errors, probeError("agents/route", perr))
		if data, ok := probeRouteErrorData(perr); ok {
			if data.AccountRoute != nil {
				result.AutoRoute = data.AccountRoute
			}
			if len(data.RouteCandidates) > 0 {
				result.RouteCandidates = data.RouteCandidates
			}
		}
	} else {
		result.RouteDecision = &route
		if route.AccountRoute != nil {
			result.AutoRoute = route.AccountRoute
		}
		if len(route.RouteCandidates) > 0 {
			result.RouteCandidates = route.RouteCandidates
		}
	}
	result.Checks = probeDataChecks(result, readiness, requireSecretBackend)
	result.NextSteps = probeDataNextSteps(result.Checks, result.Errors)
	result.OK = len(result.Errors) == 0 && allProbeChecksOK(result.Checks)
	result.Summary = probeDataSummaryFor(result, readiness, requireSecretBackend)
	result.RepairPlan = probeDataRepairPlan(result, readiness, requireSecretBackend)
	return result
}

func probeDataSummaryFor(result probeDataResult, readiness bool, requireSecretBackend string) probeDataSummary {
	accounts := []protocol.AccountCheckEvidence{}
	secretBackend := ""
	checked := 0
	accountSummary := protocol.AccountsCheckSummary{}
	if result.AccountsCheck != nil {
		accounts = result.AccountsCheck.Accounts
		secretBackend = result.AccountsCheck.SecretBackend
		checked = result.AccountsCheck.CheckedAccounts
		accountSummary = result.AccountsCheck.Summary
	}
	if secretBackend == "" {
		if raw, ok := result.Health["secretBackend"].(string); ok {
			secretBackend = raw
		}
	}
	freshQuota := 0
	staleQuota := 0
	missingQuota := 0
	for _, row := range accounts {
		switch row.QuotaState {
		case protocol.AccountQuotaStateFresh:
			freshQuota++
		case protocol.AccountQuotaStateStale:
			staleQuota++
		default:
			missingQuota++
		}
	}
	missingAccounts := 0
	if checked < 2 {
		missingAccounts = 2 - checked
	}
	summary := probeDataSummary{
		Ready:                 result.OK,
		Readiness:             readiness,
		CheckedAccounts:       accountSummary.CheckedAccounts,
		RequiredAccounts:      accountSummary.RequiredAccounts,
		MissingAccounts:       accountSummary.MissingAccounts,
		FreshQuotaAccounts:    accountSummary.FreshQuotaAccounts,
		StaleQuotaAccounts:    accountSummary.StaleQuotaAccounts,
		MissingQuotaAccounts:  accountSummary.MissingQuotaAccounts,
		AutoRouteAccountID:    accountSummary.AutoRouteAccountID,
		AutoRouteFresh:        accountSummary.AutoRouteFresh,
		RouteCandidates:       accountSummary.RouteCandidates,
		SecretBackend:         accountSummary.SecretBackend,
		RequiredSecretBackend: accountSummary.RequiredSecretBackend,
		SecretBackendOK:       accountSummary.SecretBackendOK,
		QuotaRefreshed:        accountSummary.QuotaRefreshed,
	}
	if summary.RequiredAccounts == 0 {
		summary.CheckedAccounts = checked
		summary.RequiredAccounts = 2
		summary.MissingAccounts = missingAccounts
		summary.FreshQuotaAccounts = freshQuota
		summary.StaleQuotaAccounts = staleQuota
		summary.MissingQuotaAccounts = missingQuota
		summary.SecretBackend = secretBackend
		summary.RequiredSecretBackend = requireSecretBackend
		summary.SecretBackendOK = requireSecretBackend == "" || secretBackend == requireSecretBackend
	}
	summary.RouteDecisionOK = result.RouteDecision != nil
	if len(result.RouteCandidates) > 0 {
		summary.RouteCandidates = len(result.RouteCandidates)
	}
	if result.AutoRoute != nil {
		summary.AutoRouteAccountID = result.AutoRoute.AccountID
		summary.AutoRouteFresh = result.AutoRoute.Fresh
	}
	if summary.SecretBackend == "" {
		summary.SecretBackend = secretBackend
	}
	if summary.RequiredSecretBackend == "" {
		summary.RequiredSecretBackend = requireSecretBackend
	}
	if requireSecretBackend != "" {
		summary.SecretBackendOK = summary.SecretBackend == requireSecretBackend
	}
	return summary
}

func probeDataRepairPlan(result probeDataResult, readiness bool, requireSecretBackend string) []protocol.RepairStep {
	if result.OK || !readiness {
		return nil
	}
	summary := result.Summary
	backend := requireSecretBackend
	if backend == "" {
		backend = summary.RequiredSecretBackend
	}
	if backend == "" {
		backend = summary.SecretBackend
	}
	steps := []protocol.RepairStep{}
	if summary.RequiredSecretBackend != "" && !summary.SecretBackendOK {
		steps = append(steps, protocol.RepairStep{
			ID:               "restart-daemon-secret-backend",
			Title:            "Restart capd with the required SecretStore backend",
			Command:          "capd start --secret-backend " + summary.RequiredSecretBackend,
			ExpectedEvidence: "probe summary shows secretBackendOk=true",
			RequiresSecret:   true,
		})
	}
	if summary.MissingAccounts > 0 || probeCheckFailed(result.Checks, "multi-account readiness") {
		steps = append(steps, protocol.RepairStep{
			ID:               "import-codex-accounts",
			Title:            "Import enough Codex accounts through CAP",
			Command:          "capd accounts import --auth /path/a/auth.json --auth /path/b/auth.json",
			ExpectedEvidence: "probe summary shows checkedAccounts>=2 and missingAccounts=0",
			RequiresDaemon:   true,
			RequiresSecret:   true,
		})
	}
	if probeCheckFailed(result.Checks, "account credentials") {
		steps = append(steps, protocol.RepairStep{
			ID:               "repair-secretstore",
			Title:            "Repair unreadable Codex account credentials",
			Command:          probeSecretStoreCheckCommand(backend),
			ExpectedEvidence: "probe account credentials check passes",
			RequiresSecret:   true,
		})
	}
	if summary.CheckedAccounts > 0 && (summary.FreshQuotaAccounts < summary.CheckedAccounts || !summary.AutoRouteFresh || probeCheckFailed(result.Checks, "quota freshness") || probeCheckFailed(result.Checks, "auto route fresh")) {
		steps = append(steps, protocol.RepairStep{
			ID:               "refresh-quota-readiness",
			Title:            "Refresh quota and verify daemon-side readiness",
			Command:          probeAccountsCheckReadinessCommand(backend),
			ExpectedEvidence: "probe summary shows quotaRefreshed=true and autoRouteFresh=true",
			RequiresDaemon:   true,
			RequiresSecret:   true,
		})
	}
	if !summary.RouteDecisionOK || probeCheckFailed(result.Checks, "route decision") {
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

func probeCheckFailed(checks []probeDataCheck, name string) bool {
	for _, check := range checks {
		if check.Name == name {
			return !check.OK
		}
	}
	return false
}

func probeDataChecks(result probeDataResult, readiness bool, requireSecretBackend string) []probeDataCheck {
	accounts := []protocol.AccountCheckEvidence{}
	secretBackend := ""
	checked := 0
	quotaFresh := 0
	if result.AccountsCheck != nil {
		accounts = result.AccountsCheck.Accounts
		secretBackend = result.AccountsCheck.SecretBackend
		checked = result.AccountsCheck.CheckedAccounts
	}
	for _, row := range accounts {
		if row.QuotaFresh {
			quotaFresh++
		}
	}
	if secretBackend == "" {
		if raw, ok := result.Health["secretBackend"].(string); ok {
			secretBackend = raw
		}
	}
	autoRoute := result.AutoRoute
	routeFresh := autoRoute != nil && autoRoute.Fresh
	candidates := len(result.RouteCandidates)
	routeBackend := probeRouteBackendHint(result, requireSecretBackend)
	routeReadinessCommand := probeAccountsCheckReadinessCommand(routeBackend)
	secretStates := accountSecretStatesEvidence(accounts)
	credentialReady, credentialEvidence := accountCredentialEvidence(checked, accounts)
	credentialBackend := requireSecretBackend
	if credentialBackend == "" {
		credentialBackend = secretBackend
	}
	runtimeReady, runtimeEvidence := accountRuntimeEvidence(checked, accounts)
	multiAccountImportStep := multiAccountImportNextStep()
	checks := []probeDataCheck{
		{Name: "daemon health", OK: true, Evidence: "health ok"},
		{Name: probeAccountDataCheckName(result.PromptFree), OK: result.AccountsCheck != nil, Evidence: checkedEvidence(checked, secretBackend, secretStates), NextStep: missingStep(result.AccountsCheck != nil, "start capd with account support enabled")},
		{Name: "account credentials", OK: result.PromptFree || credentialReady, Evidence: promptFreeEvidence(result.PromptFree, credentialEvidence, "not checked in prompt-free probe"), NextStep: missingStep(result.PromptFree || credentialReady, probeCredentialNextStep(accounts, credentialBackend))},
		{Name: "account runtime", OK: result.PromptFree || runtimeReady, Evidence: promptFreeEvidence(result.PromptFree, runtimeEvidence, "not checked in prompt-free probe"), NextStep: missingStep(result.PromptFree || runtimeReady, "project account runtimes with accounts/project or rerun accounts/check")},
		{Name: "multi-account readiness", OK: !readiness || checked >= 2, Evidence: countEvidence(checked, 2), NextStep: missingStep(!readiness || checked >= 2, multiAccountImportStep)},
		{Name: "quota freshness", OK: !readiness || (len(accounts) > 0 && quotaFresh == len(accounts)), Evidence: quotaFreshEvidence(quotaFresh, len(accounts)), NextStep: missingStep(!readiness || (len(accounts) > 0 && quotaFresh == len(accounts)), "refresh and verify daemon-side readiness with: "+routeReadinessCommand)},
		{Name: "auto route data", OK: autoRoute != nil, Evidence: routeEvidenceTextPtr(autoRoute), NextStep: missingStep(autoRoute != nil, "import accounts, then preview with: capd agents route --account auto --json")},
		{Name: "auto route fresh", OK: !readiness || routeFresh, Evidence: routeEvidenceTextPtr(autoRoute), NextStep: missingStep(!readiness || routeFresh, "refresh and verify daemon-side readiness with: "+routeReadinessCommand)},
		{Name: "route decision", OK: result.RouteDecision != nil, Evidence: routeDecisionEvidence(result.RouteDecision), NextStep: missingStep(result.RouteDecision != nil, "preview routing with: capd agents route --account auto --require-fresh-quota --json")},
		{Name: "route candidates", OK: candidates > 0, Evidence: countEvidence(candidates, 1), NextStep: missingStep(candidates > 0, "refresh diagnostics or run: capd agents route --account auto --json")},
	}
	if requireSecretBackend != "" {
		checks = append(checks, probeDataCheck{
			Name:     "SecretStore backend",
			OK:       secretBackend == requireSecretBackend,
			Evidence: "secret " + secretBackend + ", want " + requireSecretBackend,
			NextStep: missingStep(secretBackend == requireSecretBackend, "restart daemon with: capd start --secret-backend "+requireSecretBackend),
		})
	}
	return checks
}

func multiAccountImportNextStep() string {
	return "import at least two accounts with: capd accounts import --auth /path/a/auth.json --auth /path/b/auth.json, or batch import with: CAPD_CODEX_AUTH_PATHS=/path/a/auth.json" + string(os.PathListSeparator) + "/path/b/auth.json capd accounts import"
}

func probeAccountDataCheckName(promptFree bool) string {
	if promptFree {
		return "account metadata"
	}
	return "accounts/check data"
}

func promptFreeEvidence(promptFree bool, evidence, promptFreeText string) string {
	if promptFree {
		return promptFreeText
	}
	return evidence
}

func probeCredentialNextStep(accounts []protocol.AccountCheckEvidence, backend string) string {
	switch {
	case probeAccountsHaveSecretState(accounts, protocol.AccountSecretStateAccessDenied):
		return "macOS Keychain access was denied or canceled; approve the prompt, or restart with file SecretStore and re-import accounts: capd start --secret-backend file"
	case probeAccountsHaveSecretState(accounts, protocol.AccountSecretStateTimeout):
		return "unlock or approve OS SecretStore access, then rerun: capd probe data --json --readiness --require-secret-backend " + probeSecretBackendOrNative(backend) + " --timeout 2m --fail"
	case probeAccountsHaveSecretState(accounts, protocol.AccountSecretStateBackendMismatch):
		return "restart daemon with the account SecretStore backend or re-import affected accounts through CAP: capd accounts import --auth /path/to/auth.json"
	case probeAccountsHaveSecretState(accounts, protocol.AccountSecretStateMissing):
		return "re-import missing Codex credentials through CAP: capd accounts import --auth /path/to/auth.json"
	case probeAccountsHaveSecretState(accounts, protocol.AccountSecretStateMalformedRef):
		return "remove and re-import malformed Codex account metadata"
	case probeAccountsHaveSecretState(accounts, protocol.AccountSecretStateUnreadable):
		return "verify SecretStore directly with: " + probeSecretStoreCheckCommand(backend) + ", then re-import affected accounts through CAP: capd accounts import --auth /path/to/auth.json"
	default:
		return "fix SecretStore access or re-import failing accounts"
	}
}

func probeAccountsHaveSecretState(accounts []protocol.AccountCheckEvidence, state string) bool {
	for _, row := range accounts {
		if row.SecretState == state {
			return true
		}
	}
	return false
}

func probeSecretStoreCheckCommand(backend string) string {
	cmd := "capd secretstore check --json --roundtrip"
	if backend != "" {
		cmd += " --secret-backend " + backend + " --require-backend " + backend
	}
	return cmd + " --timeout 2m"
}

func probeSecretBackendOrNative(backend string) string {
	if backend != "" {
		return backend
	}
	return secret.BackendNative
}

func probeRouteBackendHint(result probeDataResult, fallback string) string {
	if result.AutoRoute != nil && result.AutoRoute.SecretBackend != "" {
		return result.AutoRoute.SecretBackend
	}
	for _, candidate := range result.RouteCandidates {
		if candidate.SecretBackend != "" {
			return candidate.SecretBackend
		}
	}
	return fallback
}

func probeAccountsCheckReadinessCommand(requireSecretBackend string) string {
	cmd := "capd accounts check --json --readiness"
	if requireSecretBackend != "" {
		cmd += " --require-secret-backend " + requireSecretBackend
	}
	return cmd + " --timeout 2m"
}

func allProbeChecksOK(checks []probeDataCheck) bool {
	for _, check := range checks {
		if !check.OK {
			return false
		}
	}
	return true
}

func probeDataNextSteps(checks []probeDataCheck, errors []probeDataError) []string {
	seen := map[string]bool{}
	steps := []string{}
	for _, perr := range errors {
		step := probeErrorNextStep(perr)
		if step == "" || seen[step] {
			continue
		}
		seen[step] = true
		steps = append(steps, step)
	}
	for _, check := range checks {
		step := strings.TrimSpace(check.NextStep)
		if step == "" || seen[step] {
			continue
		}
		seen[step] = true
		steps = append(steps, step)
	}
	return steps
}

func probeErrorNextStep(perr probeDataError) string {
	msg := strings.ToLower(perr.Message)
	switch {
	case strings.Contains(msg, "macos keychain status -128"):
		return "macOS Keychain denied or canceled credential access; approve the prompt, or avoid native prompts by restarting with: capd start --secret-backend file and re-importing accounts with: capd accounts --secret-backend file codex import --auth /path/to/auth.json"
	case strings.Contains(msg, "keychain"):
		return "unlock or approve OS SecretStore access, then rerun: capd probe data --json --readiness --require-secret-backend native --timeout 2m --fail"
	default:
		return ""
	}
}

func probeError(source string, perr *protocol.Error) probeDataError {
	if perr == nil {
		return probeDataError{Source: source}
	}
	return probeDataError{Source: source, Code: perr.Code, Message: perr.Message}
}

func probeRouteErrorData(perr *protocol.Error) (protocol.AgentRouteErrorData, bool) {
	if perr == nil || perr.Data == nil {
		return protocol.AgentRouteErrorData{}, false
	}
	if data, ok := perr.Data.(protocol.AgentRouteErrorData); ok {
		return data, data.AccountRoute != nil || len(data.RouteCandidates) > 0
	}
	raw, err := json.Marshal(perr.Data)
	if err != nil {
		return protocol.AgentRouteErrorData{}, false
	}
	var data protocol.AgentRouteErrorData
	if err := json.Unmarshal(raw, &data); err != nil {
		return protocol.AgentRouteErrorData{}, false
	}
	return data, data.AccountRoute != nil || len(data.RouteCandidates) > 0
}

func parseBoolQuery(r *http.Request, key string) bool {
	value := strings.TrimSpace(strings.ToLower(r.URL.Query().Get(key)))
	return value == "1" || value == "true" || value == "yes"
}

func checkedEvidence(checked int, secretBackend, secretStates string) string {
	if secretBackend == "" {
		secretBackend = "unknown"
	}
	if secretStates == "" {
		secretStates = "unknown"
	}
	return countEvidence(checked, 1) + ", secret " + secretBackend + ", secretState " + secretStates
}

func accountCredentialEvidence(checked int, accounts []protocol.AccountCheckEvidence) (bool, string) {
	readable := 0
	backendOK := 0
	for _, row := range accounts {
		if row.SecretBackendOK {
			backendOK++
		}
		if row.CredentialReadable {
			readable++
		}
	}
	return checked > 0 && len(accounts) == checked && readable == checked && backendOK == checked,
		"readable " + strconv.Itoa(readable) + "/" + strconv.Itoa(checked) + ", backend " + strconv.Itoa(backendOK) + "/" + strconv.Itoa(checked)
}

func accountRuntimeEvidence(checked int, accounts []protocol.AccountCheckEvidence) (bool, string) {
	runtime := 0
	auth := 0
	marker := 0
	for _, row := range accounts {
		if row.RuntimeReady {
			runtime++
		}
		if row.AuthJSONPrivate {
			auth++
		}
		if row.ProjectionMarkerOK {
			marker++
		}
	}
	return checked > 0 && len(accounts) == checked && runtime == checked,
		"runtime " + strconv.Itoa(runtime) + "/" + strconv.Itoa(checked) + ", auth " + strconv.Itoa(auth) + "/" + strconv.Itoa(checked) + ", marker " + strconv.Itoa(marker) + "/" + strconv.Itoa(checked)
}

func accountSecretStatesEvidence(accounts []protocol.AccountCheckEvidence) string {
	if len(accounts) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, row := range accounts {
		if row.SecretState == "" {
			continue
		}
		counts[row.SecretState]++
	}
	if len(counts) == 0 {
		return ""
	}
	order := []string{
		protocol.AccountSecretStateReadable,
		protocol.AccountSecretStateTimeout,
		protocol.AccountSecretStateBackendMismatch,
		protocol.AccountSecretStateMissing,
		protocol.AccountSecretStateMalformedRef,
		protocol.AccountSecretStateAccessDenied,
		protocol.AccountSecretStateUnreadable,
	}
	parts := []string{}
	for _, state := range order {
		if count := counts[state]; count > 0 {
			parts = append(parts, state+" "+strconv.Itoa(count))
		}
	}
	return strings.Join(parts, ", ")
}

func countEvidence(got, want int) string {
	return strconv.Itoa(got) + ", need " + strconv.Itoa(want)
}

func quotaFreshEvidence(fresh, total int) string {
	return "fresh " + strconv.Itoa(fresh) + "/" + strconv.Itoa(total)
}

func routeEvidenceTextPtr(route *protocol.AccountRouteEvidence) string {
	if route == nil {
		return "missing"
	}
	parts := []string{route.AccountID, route.QuotaState}
	if route.Fresh {
		parts = append(parts, "fresh")
	} else {
		parts = append(parts, "not-fresh")
	}
	if route.SecretBackend != "" {
		parts = append(parts, "secret "+route.SecretBackend)
	}
	if route.PrimaryUsedPercent != nil {
		parts = append(parts, "primary "+strconv.FormatFloat(*route.PrimaryUsedPercent, 'f', -1, 64)+"%")
	}
	if route.LimitingUsedPercent != nil && route.LimitingQuotaDimension != "" && route.LimitingQuotaDimension != "primary" {
		label := "limiting"
		label += " " + route.LimitingQuotaDimension
		parts = append(parts, label+" "+strconv.FormatFloat(*route.LimitingUsedPercent, 'f', -1, 64)+"%")
	}
	parts = append(parts, "score "+strconv.FormatFloat(route.Score, 'f', -1, 64))
	if route.Reason != "" {
		parts = append(parts, route.Reason)
	}
	return strings.Join(parts, " ")
}

func routeDecisionEvidence(route *protocol.AgentRouteResult) string {
	if route == nil {
		return "missing"
	}
	if route.AccountRoute != nil {
		return route.Agent.ID + " " + routeEvidenceTextPtr(route.AccountRoute)
	}
	return route.Agent.ID
}

func missingStep(ok bool, step string) string {
	if ok {
		return ""
	}
	return step
}

func writeLocalPageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self' http://127.0.0.1:* http://localhost:* http://[::1]:* ws://127.0.0.1:* ws://localhost:* ws://[::1]:*; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeLocalJSONHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
