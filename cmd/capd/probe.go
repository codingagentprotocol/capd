package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/config"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func newProbeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "probe",
		Short: "Fetch safe local web probe diagnostics",
	}
	dataCmd := &cobra.Command{
		Use:   "data",
		Short: "Fetch /probe/data with daemon token header auth",
		RunE: func(cmd *cobra.Command, _ []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			readiness, _ := cmd.Flags().GetBool("readiness")
			fail, _ := cmd.Flags().GetBool("fail")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			requireSecretBackend, _ := cmd.Flags().GetString("require-secret-backend")
			requireSecretBackend, err := secret.NormalizeBackend(requireSecretBackend)
			if err != nil {
				return err
			}
			if readiness && requireSecretBackend == "" {
				requireSecretBackend = secret.BackendNative
			}
			callCtx := cmd.Context()
			var cancel context.CancelFunc
			if timeout > 0 {
				callCtx, cancel = context.WithTimeout(callCtx, timeout)
				defer cancel()
			}
			body, status, err := daemonProbeData(callCtx, config.Load(), probeDataOptions{
				Readiness:            readiness,
				RequireSecretBackend: requireSecretBackend,
			})
			if err != nil {
				return err
			}
			var result probeDataResponse
			if err := json.Unmarshal(body, &result); err != nil {
				return fmt.Errorf("decode probe data: %w", err)
			}
			if jsonOut {
				var formatted any
				if err := json.Unmarshal(body, &formatted); err == nil {
					out, _ := json.MarshalIndent(formatted, "", "  ")
					fmt.Fprintln(cmd.OutOrStdout(), string(out))
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), string(body))
				}
			} else {
				printProbeDataText(cmd, result, status)
			}
			if fail && (!result.OK || status >= http.StatusBadRequest) {
				return fmt.Errorf("probe data failed: status %d ok=%t", status, result.OK)
			}
			return nil
		},
	}
	dataCmd.Flags().Bool("json", false, "print /probe/data JSON")
	dataCmd.Flags().Bool("readiness", false, "request the stronger readiness diagnostics view")
	dataCmd.Flags().Bool("fail", false, "exit non-zero when /probe/data reports ok=false or an HTTP error status")
	dataCmd.Flags().Duration("timeout", 2*time.Minute, "maximum time to wait for /probe/data")
	dataCmd.Flags().String("require-secret-backend", "", "request a SecretStore backend requirement for readiness diagnostics (file or native)")
	evidenceCmd := &cobra.Command{
		Use:   "evidence",
		Short: "Validate live selftest evidence manifest and artifacts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			manifestPath, _ := cmd.Flags().GetString("manifest")
			artifactPaths, _ := cmd.Flags().GetStringArray("artifact")
			jsonOut, _ := cmd.Flags().GetBool("json")
			fail, _ := cmd.Flags().GetBool("fail")
			report, err := loadProbeEvidenceReport(manifestPath, artifactPaths)
			if err != nil {
				return err
			}
			if jsonOut {
				out, _ := json.MarshalIndent(report, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
			} else {
				printProbeEvidenceReport(cmd, report)
			}
			if fail && !report.OK {
				return fmt.Errorf("probe evidence failed: %s", strings.Join(report.Issues, "; "))
			}
			return nil
		},
	}
	evidenceCmd.Flags().String("manifest", "", "path to CAPD live selftest manifest.json or summary.json")
	evidenceCmd.Flags().StringArray("artifact", nil, "path to a safe evidence artifact JSON file; repeat for agents-route/probe-data/doctor")
	evidenceCmd.Flags().Bool("json", false, "print evidence validation JSON")
	evidenceCmd.Flags().Bool("fail", false, "exit non-zero when required evidence is missing or not ready")
	_ = evidenceCmd.MarkFlagRequired("manifest")
	cmd.AddCommand(dataCmd, evidenceCmd)
	return cmd
}

type probeEvidenceReport struct {
	OK              bool                         `json:"ok"`
	Manifest        probeEvidenceManifest        `json:"manifest"`
	Artifacts       []probeEvidenceArtifact      `json:"artifacts,omitempty"`
	Checks          []probeEvidenceCheck         `json:"checks"`
	RoutePolicy     *protocol.AccountRoutePolicy `json:"routePolicy,omitempty"`
	RouteCandidates int                          `json:"routeCandidates"`
	FreshCandidates int                          `json:"freshCandidates"`
	QuotaFresh      bool                         `json:"quotaFresh"`
	RepairPlanSteps int                          `json:"repairPlanSteps"`
	Issues          []string                     `json:"issues,omitempty"`
}

type probeEvidenceCheck struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Evidence string `json:"evidence"`
	NextStep string `json:"nextStep,omitempty"`
}

type probeEvidenceManifest struct {
	Path       string            `json:"path"`
	Dir        string            `json:"dir"`
	Source     string            `json:"source"`
	Version    int               `json:"version"`
	Status     string            `json:"status"`
	Stage      string            `json:"stage"`
	Backend    string            `json:"backend"`
	DaemonMode string            `json:"daemonMode"`
	Artifacts  map[string]string `json:"artifacts,omitempty"`
}

type probeEvidenceArtifact struct {
	Path            string `json:"path"`
	Kind            string `json:"kind"`
	RoutePolicy     bool   `json:"routePolicy"`
	RouteCandidates int    `json:"routeCandidates"`
	FreshCandidates int    `json:"freshCandidates"`
	QuotaFresh      bool   `json:"quotaFresh"`
	RepairPlanSteps int    `json:"repairPlanSteps"`
}

type probeDataOptions struct {
	Readiness            bool
	RequireSecretBackend string
}

type probeDataResponse struct {
	OK         bool `json:"ok"`
	PromptFree bool `json:"promptFree"`
	Summary    struct {
		Ready                 bool   `json:"ready"`
		Readiness             bool   `json:"readiness"`
		CheckedAccounts       int    `json:"checkedAccounts"`
		RequiredAccounts      int    `json:"requiredAccounts"`
		MissingAccounts       int    `json:"missingAccounts"`
		FreshQuotaAccounts    int    `json:"freshQuotaAccounts"`
		StaleQuotaAccounts    int    `json:"staleQuotaAccounts"`
		MissingQuotaAccounts  int    `json:"missingQuotaAccounts"`
		AutoRouteAccountID    string `json:"autoRouteAccountId"`
		AutoRouteFresh        bool   `json:"autoRouteFresh"`
		RouteDecisionOK       bool   `json:"routeDecisionOk"`
		RouteCandidates       int    `json:"routeCandidates"`
		SecretBackend         string `json:"secretBackend"`
		RequiredSecretBackend string `json:"requiredSecretBackend"`
		SecretBackendOK       bool   `json:"secretBackendOk"`
		QuotaRefreshed        bool   `json:"quotaRefreshed"`
	} `json:"summary"`
	Health struct {
		Version         string `json:"version"`
		ProtocolVersion string `json:"protocolVersion"`
		SecretBackend   string `json:"secretBackend"`
	} `json:"health"`
	AccountsCheck *struct {
		Provider        string `json:"provider"`
		SecretBackend   string `json:"secretBackend"`
		CheckedAccounts int    `json:"checkedAccounts"`
	} `json:"accountsCheck"`
	AutoRoute *struct {
		AccountID     string   `json:"accountId"`
		SecretBackend string   `json:"secretBackend"`
		QuotaState    string   `json:"quotaState"`
		Fresh         bool     `json:"fresh"`
		Primary       *float64 `json:"primaryUsedPercent"`
	} `json:"autoRoute"`
	RoutePolicy     *protocol.AccountRoutePolicy `json:"routePolicy"`
	RouteCandidates []struct {
		AccountID     string   `json:"accountId"`
		SecretBackend string   `json:"secretBackend"`
		QuotaState    string   `json:"quotaState"`
		Fresh         bool     `json:"fresh"`
		Primary       *float64 `json:"primaryUsedPercent"`
		Score         float64  `json:"score"`
		Reason        string   `json:"reason"`
	} `json:"routeCandidates"`
	Checks []struct {
		Name     string `json:"name"`
		OK       bool   `json:"ok"`
		Evidence string `json:"evidence"`
		NextStep string `json:"nextStep"`
	} `json:"checks"`
	NextSteps  []string              `json:"nextSteps"`
	RepairPlan []protocol.RepairStep `json:"repairPlan"`
	Errors     []struct {
		Source  string `json:"source"`
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Error string `json:"error"`
}

func daemonProbeData(ctx context.Context, cfg config.Config, opts probeDataOptions) ([]byte, int, error) {
	token, err := readDaemonToken()
	if err != nil {
		return nil, 0, err
	}
	u := url.URL{
		Scheme: "http",
		Host:   daemonAddr(cfg),
		Path:   "/probe/data",
	}
	q := u.Query()
	if opts.Readiness {
		q.Set("readiness", "1")
	}
	if opts.RequireSecretBackend != "" {
		q.Set("requireSecretBackend", opts.RequireSecretBackend)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, daemonConnectError(cfg, token, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, resp.StatusCode, fmt.Errorf("probe data unauthorized (is the daemon token current?)")
	}
	if len(body) == 0 {
		return nil, resp.StatusCode, fmt.Errorf("probe data returned empty body with status %d", resp.StatusCode)
	}
	return body, resp.StatusCode, nil
}

func printProbeDataText(cmd *cobra.Command, result probeDataResponse, status int) {
	fmt.Fprintf(cmd.OutOrStdout(), "status: %d\n", status)
	fmt.Fprintf(cmd.OutOrStdout(), "ok: %t\n", result.OK)
	if result.PromptFree {
		fmt.Fprintln(cmd.OutOrStdout(), "mode: prompt-free account metadata (SecretStore and runtime not checked)")
	}
	health := []string{}
	if result.Health.Version != "" {
		health = append(health, "version "+result.Health.Version)
	}
	if result.Health.ProtocolVersion != "" {
		health = append(health, "protocol "+result.Health.ProtocolVersion)
	}
	if result.Health.SecretBackend != "" {
		health = append(health, "secret "+result.Health.SecretBackend)
	}
	if len(health) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "health: %s\n", strings.Join(health, ", "))
	}
	if result.AccountsCheck != nil {
		label := "accounts/check"
		if result.PromptFree {
			label = "account metadata"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %d checked, secret %s\n", label, result.AccountsCheck.CheckedAccounts, result.AccountsCheck.SecretBackend)
	}
	if result.Summary.RequiredAccounts > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "summary: ready=%t accounts=%d/%d missing=%d quota fresh=%d stale=%d missing=%d autoFresh=%t routeDecision=%t routeCandidates=%d secretOK=%t\n",
			result.Summary.Ready,
			result.Summary.CheckedAccounts,
			result.Summary.RequiredAccounts,
			result.Summary.MissingAccounts,
			result.Summary.FreshQuotaAccounts,
			result.Summary.StaleQuotaAccounts,
			result.Summary.MissingQuotaAccounts,
			result.Summary.AutoRouteFresh,
			result.Summary.RouteDecisionOK,
			result.Summary.RouteCandidates,
			result.Summary.SecretBackendOK,
		)
	}
	if result.Summary.SecretBackend != "" || result.Summary.RequiredSecretBackend != "" {
		required := result.Summary.RequiredSecretBackend
		if required == "" {
			required = "none"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "secret backend: actual=%s required=%s ok=%t\n", result.Summary.SecretBackend, required, result.Summary.SecretBackendOK)
	}
	if result.AutoRoute != nil {
		backend := ""
		if result.AutoRoute.SecretBackend != "" {
			backend = " secret=" + result.AutoRoute.SecretBackend
		}
		fmt.Fprintf(cmd.OutOrStdout(), "auto route: %s %s fresh=%t%s\n", result.AutoRoute.AccountID, result.AutoRoute.QuotaState, result.AutoRoute.Fresh, backend)
	}
	if result.RoutePolicy != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "route policy: %s\n", routePolicyText(*result.RoutePolicy))
	}
	if len(result.RouteCandidates) > 0 {
		parts := make([]string, 0, len(result.RouteCandidates))
		for _, candidate := range result.RouteCandidates {
			part := fmt.Sprintf("%s %s fresh=%t", candidate.AccountID, candidate.QuotaState, candidate.Fresh)
			if candidate.SecretBackend != "" {
				part += " secret=" + candidate.SecretBackend
			}
			if candidate.Primary != nil {
				part += fmt.Sprintf(" primary=%.1f%%", *candidate.Primary)
			}
			if candidate.Reason != "" {
				part += " " + candidate.Reason
			}
			parts = append(parts, part)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "route candidates: %s\n", strings.Join(parts, "; "))
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "CHECK\tOK\tEVIDENCE\tNEXT_STEP")
	for _, check := range result.Checks {
		fmt.Fprintf(w, "%s\t%t\t%s\t%s\n", check.Name, check.OK, check.Evidence, check.NextStep)
	}
	_ = w.Flush()
	for _, probeErr := range result.Errors {
		fmt.Fprintf(cmd.OutOrStdout(), "error: %s code=%d %s\n", probeErr.Source, probeErr.Code, probeErr.Message)
	}
	for _, next := range result.NextSteps {
		fmt.Fprintf(cmd.OutOrStdout(), "next: %s\n", next)
	}
	if len(result.RepairPlan) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "repair plan:")
		for i, step := range result.RepairPlan {
			fmt.Fprintf(cmd.OutOrStdout(), "%d. %s\n", i+1, step.Title)
			fmt.Fprintf(cmd.OutOrStdout(), "   command: %s\n", step.Command)
			fmt.Fprintf(cmd.OutOrStdout(), "   expect: %s\n", step.ExpectedEvidence)
		}
	}
	if result.Error != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "error: probe data %s\n", result.Error)
	}
}

func loadProbeEvidenceReport(manifestPath string, artifactPaths []string) (probeEvidenceReport, error) {
	manifest, err := loadProbeEvidenceManifest(manifestPath)
	if err != nil {
		return probeEvidenceReport{}, err
	}
	report := probeEvidenceReport{Manifest: manifest}
	if len(artifactPaths) == 0 {
		artifactPaths = manifestArtifactPaths(manifest)
	}
	for _, path := range artifactPaths {
		artifact, err := loadProbeEvidenceArtifact(path)
		if err != nil {
			return probeEvidenceReport{}, err
		}
		report.Artifacts = append(report.Artifacts, artifact)
		if artifact.RouteCandidates > 0 {
			report.RouteCandidates += artifact.RouteCandidates
			report.FreshCandidates += artifact.FreshCandidates
		}
		if artifact.RepairPlanSteps > 0 {
			report.RepairPlanSteps += artifact.RepairPlanSteps
		}
	}
	for _, artifact := range report.Artifacts {
		if artifact.RoutePolicy && report.RoutePolicy == nil {
			policy, _ := readRoutePolicyFromFile(artifact.Path)
			report.RoutePolicy = policy
		}
		if artifact.QuotaFresh {
			report.QuotaFresh = true
		}
	}
	report.Checks = probeEvidenceChecks(report)
	report.Issues = probeEvidenceIssues(report.Checks)
	report.OK = len(report.Issues) == 0
	return report, nil
}

func manifestArtifactPaths(manifest probeEvidenceManifest) []string {
	seen := map[string]bool{}
	paths := []string{}
	for _, key := range []string{"agentsRoute", "probeData", "doctor", "accountsSmoke", "accountsCheck", "health", "accountsList"} {
		path := resolveManifestArtifactPath(manifest, manifest.Artifacts[key])
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}

func resolveManifestArtifactPath(manifest probeEvidenceManifest, path string) string {
	if path == "" || filepath.IsAbs(path) || manifest.Dir == "" {
		return path
	}
	return filepath.Join(manifest.Dir, path)
}

func loadProbeEvidenceManifest(path string) (probeEvidenceManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return probeEvidenceManifest{}, err
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return probeEvidenceManifest{}, fmt.Errorf("decode evidence manifest: %w", err)
	}
	manifest := probeEvidenceManifest{Path: path, Dir: filepath.Dir(path), Artifacts: map[string]string{}}
	if version, ok := intField(data, "manifestVersion"); ok {
		manifest.Source = "manifest.json"
		manifest.Version = version
		manifest.Status = stringField(data, "status")
		manifest.Stage = stringField(data, "stage")
		manifest.Backend = stringField(data, "backend")
		manifest.DaemonMode = stringField(data, "daemonMode")
		if artifacts, ok := data["artifacts"].(map[string]any); ok {
			for key, value := range artifacts {
				if path, ok := value.(string); ok && path != "" {
					manifest.Artifacts[key] = path
				}
			}
		}
		return manifest, nil
	}
	if version, ok := intField(data, "summaryVersion"); ok {
		manifest.Source = "summary.json"
		manifest.Version = version
		manifest.Status = stringField(data, "status")
		manifest.Stage = stringField(data, "stage")
		manifest.Backend = stringField(data, "backend")
		manifest.DaemonMode = stringField(data, "daemonMode")
		for key, field := range map[string]string{
			"manifest":    "evidenceManifestPath",
			"agentsRoute": "routeEvidencePath",
			"probeData":   "probeEvidencePath",
			"doctor":      "doctorEvidencePath",
			"repairPlan":  "repairPlanPath",
		} {
			if value := stringField(data, field); value != "" {
				manifest.Artifacts[key] = value
			}
		}
		return manifest, nil
	}
	return probeEvidenceManifest{}, fmt.Errorf("evidence manifest must include manifestVersion or summaryVersion")
}

func loadProbeEvidenceArtifact(path string) (probeEvidenceArtifact, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return probeEvidenceArtifact{}, err
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return probeEvidenceArtifact{}, fmt.Errorf("decode evidence artifact %s: %w", path, err)
	}
	artifact := probeEvidenceArtifact{Path: path, Kind: probeEvidenceArtifactKind(data)}
	artifact.RoutePolicy = mapHasNested(data, "routePolicy") || mapHasNested(data, "codex", "routePolicy") || mapHasNested(data, "data", "routePolicy")
	candidates := firstArray(data, []string{"routeCandidates"}, []string{"data", "routeCandidates"}, []string{"codex", "routeCandidates"})
	artifact.RouteCandidates = len(candidates)
	for _, candidate := range candidates {
		if row, ok := candidate.(map[string]any); ok {
			if fresh, ok := row["fresh"].(bool); ok && fresh {
				artifact.FreshCandidates++
			}
		}
	}
	summary := firstMap(data, []string{"summary"}, []string{"data", "summary"})
	if summary != nil {
		checked, _ := intField(summary, "checkedAccounts")
		fresh, _ := intField(summary, "freshQuotaAccounts")
		autoFresh, _ := summary["autoRouteFresh"].(bool)
		artifact.QuotaFresh = checked > 0 && fresh == checked && autoFresh
	} else if artifact.RouteCandidates > 0 {
		artifact.QuotaFresh = artifact.FreshCandidates == artifact.RouteCandidates
	}
	repair := firstArray(data, []string{"repairPlan"}, []string{"data", "repairPlan"}, []string{"codex", "repairPlan"})
	artifact.RepairPlanSteps = len(repair)
	return artifact, nil
}

func readRoutePolicyFromFile(path string) (*protocol.AccountRoutePolicy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var holder struct {
		RoutePolicy *protocol.AccountRoutePolicy `json:"routePolicy"`
		Codex       struct {
			RoutePolicy *protocol.AccountRoutePolicy `json:"routePolicy"`
		} `json:"codex"`
		Data struct {
			RoutePolicy *protocol.AccountRoutePolicy `json:"routePolicy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &holder); err != nil {
		return nil, err
	}
	switch {
	case holder.RoutePolicy != nil:
		return holder.RoutePolicy, nil
	case holder.Data.RoutePolicy != nil:
		return holder.Data.RoutePolicy, nil
	case holder.Codex.RoutePolicy != nil:
		return holder.Codex.RoutePolicy, nil
	default:
		return nil, nil
	}
}

func probeEvidenceArtifactKind(data map[string]any) string {
	switch {
	case mapHasNested(data, "routePolicy") || mapHasNested(data, "routeCandidates"):
		return "route"
	case mapHasNested(data, "summary") || mapHasNested(data, "checks"):
		return "probe"
	case mapHasNested(data, "codex"):
		return "doctor"
	default:
		return "unknown"
	}
}

func printProbeEvidenceReport(cmd *cobra.Command, report probeEvidenceReport) {
	fmt.Fprintf(cmd.OutOrStdout(), "ok: %t\n", report.OK)
	fmt.Fprintf(cmd.OutOrStdout(), "manifest: %s %s status=%s stage=%s backend=%s daemon=%s\n", report.Manifest.Source, report.Manifest.Path, report.Manifest.Status, report.Manifest.Stage, report.Manifest.Backend, report.Manifest.DaemonMode)
	if report.RoutePolicy != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "route policy: %s\n", routePolicyText(*report.RoutePolicy))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "route candidates: %d fresh=%d\n", report.RouteCandidates, report.FreshCandidates)
	fmt.Fprintf(cmd.OutOrStdout(), "quota fresh: %t\n", report.QuotaFresh)
	fmt.Fprintf(cmd.OutOrStdout(), "repair plan: %d steps\n", report.RepairPlanSteps)
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ARTIFACT\tKIND\tPOLICY\tCANDIDATES\tFRESH\tQUOTA\tREPAIR")
	for _, artifact := range report.Artifacts {
		fmt.Fprintf(w, "%s\t%s\t%t\t%d\t%d\t%t\t%d\n", artifact.Path, artifact.Kind, artifact.RoutePolicy, artifact.RouteCandidates, artifact.FreshCandidates, artifact.QuotaFresh, artifact.RepairPlanSteps)
	}
	_ = w.Flush()
	checks := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
	fmt.Fprintln(checks, "CHECK\tOK\tEVIDENCE\tNEXT_STEP")
	for _, check := range report.Checks {
		fmt.Fprintf(checks, "%s\t%t\t%s\t%s\n", check.Name, check.OK, check.Evidence, check.NextStep)
	}
	_ = checks.Flush()
	for _, issue := range report.Issues {
		fmt.Fprintf(cmd.OutOrStdout(), "issue: %s\n", issue)
	}
}

func probeEvidenceChecks(report probeEvidenceReport) []probeEvidenceCheck {
	return []probeEvidenceCheck{
		{
			Name:     "selftest status",
			OK:       report.Manifest.Status == "passed",
			Evidence: emptyAs(report.Manifest.Status, "missing"),
			NextStep: missingEvidenceStep(report.Manifest.Status == "passed", "rerun make live-codex-selftest and inspect the saved artifacts"),
		},
		{
			Name:     "SecretStore backend",
			OK:       report.Manifest.Backend != "",
			Evidence: emptyAs(report.Manifest.Backend, "missing"),
			NextStep: missingEvidenceStep(report.Manifest.Backend != "", "rerun selftest so backend evidence is recorded"),
		},
		{
			Name:     "daemon mode",
			OK:       report.Manifest.DaemonMode != "",
			Evidence: emptyAs(report.Manifest.DaemonMode, "missing"),
			NextStep: missingEvidenceStep(report.Manifest.DaemonMode != "", "rerun selftest so daemon mode is recorded"),
		},
		{
			Name:     "route policy",
			OK:       report.RoutePolicy != nil,
			Evidence: routePolicyEvidence(report.RoutePolicy),
			NextStep: missingEvidenceStep(report.RoutePolicy != nil, "include agents-route.json, probe-data-readiness.json, or doctor-prompt-free.json"),
		},
		{
			Name:     "route candidates",
			OK:       report.RouteCandidates > 0,
			Evidence: fmt.Sprintf("%d candidates, %d fresh", report.RouteCandidates, report.FreshCandidates),
			NextStep: missingEvidenceStep(report.RouteCandidates > 0, "include agents-route.json or probe-data-readiness.json"),
		},
		{
			Name:     "quota freshness",
			OK:       report.QuotaFresh,
			Evidence: fmt.Sprintf("quotaFresh=%t", report.QuotaFresh),
			NextStep: missingEvidenceStep(report.QuotaFresh, "refresh quota and rerun make live-codex-selftest"),
		},
	}
}

func probeEvidenceIssues(checks []probeEvidenceCheck) []string {
	issues := []string{}
	for _, check := range checks {
		if !check.OK {
			issues = append(issues, check.EvidenceIssue())
		}
	}
	return issues
}

func (check probeEvidenceCheck) EvidenceIssue() string {
	switch check.Name {
	case "selftest status":
		return "selftest status is " + check.Evidence
	case "SecretStore backend":
		return "SecretStore backend missing"
	case "daemon mode":
		return "daemon mode missing"
	case "route policy":
		return "routePolicy evidence missing"
	case "route candidates":
		return "routeCandidates evidence missing"
	case "quota freshness":
		return "fresh quota evidence missing"
	default:
		return check.Name + " failed"
	}
}

func routePolicyEvidence(policy *protocol.AccountRoutePolicy) string {
	if policy == nil {
		return "missing"
	}
	return routePolicyText(*policy)
}

func missingEvidenceStep(ok bool, step string) string {
	if ok {
		return ""
	}
	return step
}

func stringField(data map[string]any, key string) string {
	if value, ok := data[key].(string); ok {
		return value
	}
	return ""
}

func intField(data map[string]any, key string) (int, bool) {
	switch value := data[key].(type) {
	case float64:
		return int(value), true
	case int:
		return value, true
	default:
		return 0, false
	}
}

func mapHasNested(data map[string]any, path ...string) bool {
	return nestedValue(data, path...) != nil
}

func firstMap(data map[string]any, paths ...[]string) map[string]any {
	for _, path := range paths {
		if value, ok := nestedValue(data, path...).(map[string]any); ok {
			return value
		}
	}
	return nil
}

func firstArray(data map[string]any, paths ...[]string) []any {
	for _, path := range paths {
		if value, ok := nestedValue(data, path...).([]any); ok {
			return value
		}
	}
	return nil
}

func nestedValue(data map[string]any, path ...string) any {
	var current any = data
	for _, key := range path {
		row, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = row[key]
	}
	return current
}

func emptyAs(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
