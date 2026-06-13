package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/config"
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
	cmd.AddCommand(dataCmd)
	return cmd
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
	NextSteps  []string `json:"nextSteps"`
	RepairPlan []struct {
		ID               string `json:"id"`
		Title            string `json:"title"`
		Command          string `json:"command"`
		ExpectedEvidence string `json:"expectedEvidence"`
		RequiresDaemon   bool   `json:"requiresDaemon"`
		RequiresSecret   bool   `json:"requiresSecret"`
		Optional         bool   `json:"optional"`
	} `json:"repairPlan"`
	Errors []struct {
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
