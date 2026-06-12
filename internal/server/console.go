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
	Health          map[string]any                  `json:"health"`
	AccountsCheck   *protocol.AccountsCheckResult   `json:"accountsCheck,omitempty"`
	RouteDecision   *protocol.AgentRouteResult      `json:"routeDecision,omitempty"`
	AutoRoute       *protocol.AccountRouteEvidence  `json:"autoRoute,omitempty"`
	RouteCandidates []protocol.AccountRouteEvidence `json:"routeCandidates,omitempty"`
	Checks          []probeDataCheck                `json:"checks"`
	Errors          []probeDataError                `json:"errors,omitempty"`
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
	return result
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
	checks := []probeDataCheck{
		{Name: "daemon health", OK: true, Evidence: "health ok"},
		{Name: "accounts/check data", OK: result.AccountsCheck != nil, Evidence: checkedEvidence(checked, secretBackend), NextStep: missingStep(result.AccountsCheck != nil, "start capd with account support enabled")},
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
			NextStep: missingStep(secretBackend == requireSecretBackend, "restart daemon with: CAPD_SECRET_BACKEND="+requireSecretBackend+" capd start"),
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

func parseBoolQuery(r *http.Request, key string) bool {
	value := strings.TrimSpace(strings.ToLower(r.URL.Query().Get(key)))
	return value == "1" || value == "true" || value == "yes"
}

func checkedEvidence(checked int, secretBackend string) string {
	if secretBackend == "" {
		secretBackend = "unknown"
	}
	return countEvidence(checked, 1) + ", secret " + secretBackend
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
