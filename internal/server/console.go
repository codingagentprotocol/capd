package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codingagentprotocol/capd/internal/account/secret"
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
	Summary         probeDataSummary                `json:"summary"`
	Health          map[string]any                  `json:"health"`
	AccountsCheck   *protocol.AccountsCheckResult   `json:"accountsCheck,omitempty"`
	RouteDecision   *protocol.AgentRouteResult      `json:"routeDecision,omitempty"`
	AutoRoute       *protocol.AccountRouteEvidence  `json:"autoRoute,omitempty"`
	RouteCandidates []protocol.AccountRouteEvidence `json:"routeCandidates,omitempty"`
	Checks          []probeDataCheck                `json:"checks"`
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
	if readiness {
		params.RefreshQuota = true
		params.RequireMultiple = true
		params.RequireFreshQuota = true
		params.RequireAllFreshQuota = true
		params.RequireSecretBackend = requireSecretBackend
	}
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
	result.OK = len(result.Errors) == 0 && allProbeChecksOK(result.Checks)
	result.Summary = probeDataSummaryFor(result, readiness, requireSecretBackend)
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
	secretStates := accountSecretStatesEvidence(accounts)
	credentialReady, credentialEvidence := accountCredentialEvidence(checked, accounts)
	runtimeReady, runtimeEvidence := accountRuntimeEvidence(checked, accounts)
	checks := []probeDataCheck{
		{Name: "daemon health", OK: true, Evidence: "health ok"},
		{Name: "accounts/check data", OK: result.AccountsCheck != nil, Evidence: checkedEvidence(checked, secretBackend, secretStates), NextStep: missingStep(result.AccountsCheck != nil, "start capd with account support enabled")},
		{Name: "account credentials", OK: credentialReady, Evidence: credentialEvidence, NextStep: missingStep(credentialReady, "fix SecretStore access or re-import failing accounts")},
		{Name: "account runtime", OK: runtimeReady, Evidence: runtimeEvidence, NextStep: missingStep(runtimeReady, "project account runtimes with accounts/project or rerun accounts/check")},
		{Name: "multi-account readiness", OK: !readiness || checked >= 2, Evidence: countEvidence(checked, 2), NextStep: missingStep(!readiness || checked >= 2, "import at least two accounts with: capd accounts import --auth /path/a --auth /path/b")},
		{Name: "quota freshness", OK: !readiness || (len(accounts) > 0 && quotaFresh == len(accounts)), Evidence: quotaFreshEvidence(quotaFresh, len(accounts)), NextStep: missingStep(!readiness || (len(accounts) > 0 && quotaFresh == len(accounts)), "refresh quota with: capd accounts check --readiness")},
		{Name: "auto route data", OK: autoRoute != nil, Evidence: routeEvidenceTextPtr(autoRoute), NextStep: missingStep(autoRoute != nil, "import accounts, then preview with: capd agents route --account auto --json")},
		{Name: "auto route fresh", OK: !readiness || routeFresh, Evidence: routeEvidenceTextPtr(autoRoute), NextStep: missingStep(!readiness || routeFresh, "refresh quota with: capd accounts check --readiness")},
		{Name: "route decision", OK: result.RouteDecision != nil, Evidence: routeDecisionEvidence(result.RouteDecision), NextStep: missingStep(result.RouteDecision != nil, "preview routing with: capd agents route --account auto")},
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

func allProbeChecksOK(checks []probeDataCheck) bool {
	for _, check := range checks {
		if !check.OK {
			return false
		}
	}
	return true
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
	if route.PrimaryUsedPercent != nil {
		parts = append(parts, "primary "+strconv.FormatFloat(*route.PrimaryUsedPercent, 'f', -1, 64)+"%")
	}
	parts = append(parts, "score "+strconv.FormatFloat(route.Score, 'f', -1, 64))
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
