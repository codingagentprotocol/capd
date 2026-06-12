package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/config"
	"github.com/codingagentprotocol/capd/internal/daemon"
	"github.com/codingagentprotocol/capd/internal/discovery"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run a local readiness preflight for capd, Codex accounts, and routing",
		RunE: func(cmd *cobra.Command, _ []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			failOnIssues, _ := cmd.Flags().GetBool("fail")
			requireSecretBackend, _ := cmd.Flags().GetString("require-secret-backend")
			requireSecretBackend, err := secret.NormalizeBackend(requireSecretBackend)
			if err != nil {
				return err
			}
			report, err := buildDoctorReport(cmd.Context(), doctorOptions{
				RequireSecretBackend: requireSecretBackend,
			})
			if err != nil {
				return err
			}
			if jsonOut {
				out, _ := json.MarshalIndent(report, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				if failOnIssues && !report.OK {
					return fmt.Errorf("doctor found %d readiness issue(s)", len(report.Issues))
				}
				return nil
			}
			printDoctorReport(cmd, report)
			if !report.OK {
				return fmt.Errorf("doctor found %d readiness issue(s)", len(report.Issues))
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "print machine-readable readiness evidence without token material")
	cmd.Flags().Bool("fail", false, "return a non-zero exit code when readiness issues are found, including with --json")
	cmd.Flags().String("require-secret-backend", "", "fail unless this SecretStore backend is active (file or native)")
	return cmd
}

type doctorOptions struct {
	RequireSecretBackend string
}

type doctorReport struct {
	OK         bool                `json:"ok"`
	Daemon     doctorDaemonReport  `json:"daemon"`
	Agents     []doctorAgentReport `json:"agents"`
	Codex      doctorCodexReport   `json:"codex"`
	Checks     []doctorCheckReport `json:"checks"`
	Issues     []string            `json:"issues,omitempty"`
	NextSteps  []string            `json:"nextSteps,omitempty"`
	CheckedAt  int64               `json:"checkedAt"`
	HealthAddr string              `json:"healthAddr"`
}

type doctorDaemonReport struct {
	OK    bool   `json:"ok"`
	Addr  string `json:"addr"`
	Error string `json:"error,omitempty"`
}

type doctorAgentReport struct {
	ID        string `json:"id"`
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Bin       string `json:"bin,omitempty"`
}

type doctorCodexReport struct {
	CLIAvailable          bool                       `json:"cliAvailable"`
	ImportedAccounts      int                        `json:"importedAccounts"`
	CurrentAccountID      string                     `json:"currentAccountId,omitempty"`
	SecretBackend         string                     `json:"secretBackend,omitempty"`
	DaemonCheckOK         bool                       `json:"daemonCheckOk"`
	DaemonCheckedAccounts int                        `json:"daemonCheckedAccounts,omitempty"`
	DaemonSecretBackend   string                     `json:"daemonSecretBackend,omitempty"`
	DaemonCheckError      string                     `json:"daemonCheckError,omitempty"`
	FreshQuotaAccounts    int                        `json:"freshQuotaAccounts"`
	StaleQuotaAccounts    int                        `json:"staleQuotaAccounts"`
	MissingQuotaAccounts  int                        `json:"missingQuotaAccounts"`
	Accounts              []doctorCodexAccountReport `json:"accounts,omitempty"`
	AutoRouteAccountID    string                     `json:"autoRouteAccountId,omitempty"`
	AutoRouteQuotaState   string                     `json:"autoRouteQuotaState,omitempty"`
	AutoRouteFresh        bool                       `json:"autoRouteFresh"`
	AutoRouteScore        float64                    `json:"autoRouteScore,omitempty"`
	AutoRouteReason       string                     `json:"autoRouteReason,omitempty"`
	AutoRouteCheckedAt    int64                      `json:"autoRouteCheckedAt,omitempty"`
	AutoRoutePrimary      *float64                   `json:"autoRoutePrimaryUsedPercent,omitempty"`
}

type doctorCodexAccountReport struct {
	ID                 string   `json:"id"`
	Email              string   `json:"email,omitempty"`
	Current            bool     `json:"current,omitempty"`
	Plan               string   `json:"plan,omitempty"`
	QuotaState         string   `json:"quotaState"`
	QuotaFresh         bool     `json:"quotaFresh"`
	QuotaCheckedAt     int64    `json:"quotaCheckedAt,omitempty"`
	PrimaryUsedPercent *float64 `json:"primaryUsedPercent,omitempty"`
}

type doctorCheckReport struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Evidence string `json:"evidence"`
	NextStep string `json:"nextStep,omitempty"`
}

const doctorReadinessCommand = "capd accounts check --json --readiness"

func buildDoctorReport(ctx context.Context, opts doctorOptions) (doctorReport, error) {
	cfg := config.Load()
	report := doctorReport{
		CheckedAt:  time.Now().Unix(),
		HealthAddr: daemonAddr(cfg),
		Daemon: doctorDaemonReport{
			Addr: daemonAddr(cfg),
		},
	}

	healthCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	_, healthErr := daemonHealth(healthCtx, cfg)
	cancel()
	if healthErr != nil {
		report.Daemon.Error = healthErr.Error()
		report.Issues = append(report.Issues, "daemon health check failed")
		report.NextSteps = append(report.NextSteps, "start the daemon with: capd start")
	} else {
		report.Daemon.OK = true
	}
	report.Checks = append(report.Checks, doctorCheckReport{
		Name:     "daemon health",
		OK:       report.Daemon.OK,
		Evidence: doctorBoolEvidence(report.Daemon.OK, "daemon /healthz ok", "daemon /healthz failed"),
		NextStep: doctorCheckNextStep(!report.Daemon.OK, "start the daemon with: capd start"),
	})

	infos := discovery.Discover(ctx, daemon.Registry())
	for _, info := range infos {
		row := doctorAgentReport{
			ID:        info.ID,
			Available: info.Available,
			Version:   info.Version,
			Bin:       info.Bin,
		}
		report.Agents = append(report.Agents, row)
		if info.ID == "codex" && info.Available {
			report.Codex.CLIAvailable = true
		}
	}
	if !report.Codex.CLIAvailable {
		report.Issues = append(report.Issues, "Codex CLI is not available")
		report.NextSteps = append(report.NextSteps, "install Codex CLI or put codex on PATH")
	}
	report.Checks = append(report.Checks, doctorCheckReport{
		Name:     "Codex CLI",
		OK:       report.Codex.CLIAvailable,
		Evidence: doctorBoolEvidence(report.Codex.CLIAvailable, "codex CLI available", "codex CLI unavailable"),
		NextStep: doctorCheckNextStep(!report.Codex.CLIAvailable, "install Codex CLI or put codex on PATH"),
	})

	accounts, secrets, err := openAccountDeps()
	if err != nil {
		return doctorReport{}, err
	}
	defer accounts.Close()
	report.Codex.SecretBackend = secrets.Backend()
	if opts.RequireSecretBackend != "" && opts.RequireSecretBackend != report.Codex.SecretBackend {
		report.Issues = append(report.Issues, fmt.Sprintf("secret backend is %q, want %q", report.Codex.SecretBackend, opts.RequireSecretBackend))
		report.NextSteps = append(report.NextSteps, fmt.Sprintf("set CAPD_SECRET_BACKEND=%s or pass --secret-backend %s for account commands", opts.RequireSecretBackend, opts.RequireSecretBackend))
	}
	secretOK := opts.RequireSecretBackend == "" || opts.RequireSecretBackend == report.Codex.SecretBackend
	report.Checks = append(report.Checks, doctorCheckReport{
		Name:     "SecretStore backend",
		OK:       secretOK,
		Evidence: fmt.Sprintf("secret backend %s", report.Codex.SecretBackend),
		NextStep: doctorCheckNextStep(!secretOK, fmt.Sprintf("set CAPD_SECRET_BACKEND=%s or pass --secret-backend %s for account commands", opts.RequireSecretBackend, opts.RequireSecretBackend)),
	})
	current, err := accounts.CurrentAccount(codexauth.Provider)
	if err != nil {
		return doctorReport{}, err
	}
	report.Codex.CurrentAccountID = current
	list, err := accounts.ListAccounts(codexauth.Provider)
	if err != nil {
		return doctorReport{}, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	report.Codex.ImportedAccounts = len(list)
	if len(list) == 0 {
		report.Issues = append(report.Issues, "no imported Codex accounts")
		report.NextSteps = append(report.NextSteps, doctorImportNextStep(report.Daemon.OK))
	}
	if len(list) < 2 {
		report.Issues = append(report.Issues, "multi-account readiness requires at least two imported Codex accounts")
		report.NextSteps = append(report.NextSteps, doctorSecondImportNextStep(report.Daemon.OK))
	}
	report.Checks = append(report.Checks, doctorCheckReport{
		Name:     "Codex multi-account import",
		OK:       len(list) >= 2,
		Evidence: fmt.Sprintf("imported %d Codex account(s)", len(list)),
		NextStep: doctorCheckNextStep(len(list) < 2, doctorAccountImportCheckNextStep(len(list), report.Daemon.OK)),
	})

	for _, acc := range list {
		row := doctorCodexAccountReport{
			ID:         acc.ID,
			Email:      acc.Email,
			Current:    acc.ID == current,
			Plan:       acc.Plan,
			QuotaState: protocol.AccountQuotaStateMissing,
		}
		q, err := accounts.LoadQuota(acc.ID)
		if err != nil {
			report.Codex.MissingQuotaAccounts++
			report.Codex.Accounts = append(report.Codex.Accounts, row)
			continue
		}
		if row.Plan == "" {
			row.Plan = q.Plan
		}
		row.QuotaState = accountQuotaState(q)
		row.QuotaFresh = row.QuotaState == protocol.AccountQuotaStateFresh
		row.QuotaCheckedAt = q.CheckedAt
		row.PrimaryUsedPercent = &q.PrimaryUsedPercent
		switch row.QuotaState {
		case protocol.AccountQuotaStateFresh:
			report.Codex.FreshQuotaAccounts++
		case protocol.AccountQuotaStateStale:
			report.Codex.StaleQuotaAccounts++
		default:
			report.Codex.MissingQuotaAccounts++
		}
		report.Codex.Accounts = append(report.Codex.Accounts, row)
	}
	if len(list) > 0 && report.Codex.FreshQuotaAccounts < len(list) {
		report.Issues = append(report.Issues, "not every imported Codex account has fresh quota evidence")
		report.NextSteps = append(report.NextSteps, doctorReadinessNextStep())
	}
	allQuotaFresh := len(list) > 0 && report.Codex.FreshQuotaAccounts == len(list)
	report.Checks = append(report.Checks, doctorCheckReport{
		Name:     "Codex quota freshness",
		OK:       allQuotaFresh,
		Evidence: fmt.Sprintf("fresh %d/%d, stale %d, missing %d", report.Codex.FreshQuotaAccounts, len(list), report.Codex.StaleQuotaAccounts, report.Codex.MissingQuotaAccounts),
		NextStep: doctorCheckNextStep(!allQuotaFresh, doctorQuotaCheckNextStep(len(list), report.Daemon.OK)),
	})
	if len(list) > 0 {
		route, err := account.SelectQuotaRouteAccount(accounts, codexauth.Provider)
		if err != nil {
			report.Issues = append(report.Issues, "auto account routing could not select a Codex account")
		} else {
			evidence := account.QuotaRouteEvidence(accounts, route)
			report.Codex.AutoRouteAccountID = evidence.AccountID
			report.Codex.AutoRouteQuotaState = evidence.QuotaState
			report.Codex.AutoRouteFresh = evidence.Fresh
			report.Codex.AutoRouteScore = evidence.Score
			report.Codex.AutoRouteCheckedAt = evidence.CheckedAt
			report.Codex.AutoRoutePrimary = evidence.PrimaryUsedPercent
			report.Codex.AutoRouteReason = account.QuotaRouteReason(accounts, route)
			if !report.Codex.AutoRouteFresh {
				report.Issues = append(report.Issues, "auto account route is not backed by fresh quota")
				report.NextSteps = append(report.NextSteps, doctorRouteReadinessNextStep())
			}
		}
	}
	autoRouteOK := report.Codex.AutoRouteAccountID != "" && report.Codex.AutoRouteFresh
	autoRouteEvidence := "auto route missing"
	if report.Codex.AutoRouteAccountID != "" {
		autoRouteEvidence = fmt.Sprintf("auto route %s quota=%s fresh=%t", report.Codex.AutoRouteAccountID, report.Codex.AutoRouteQuotaState, report.Codex.AutoRouteFresh)
	}
	report.Checks = append(report.Checks, doctorCheckReport{
		Name:     "Codex auto route freshness",
		OK:       autoRouteOK,
		Evidence: autoRouteEvidence,
		NextStep: doctorCheckNextStep(!autoRouteOK, doctorRouteCheckNextStep(len(list), report.Daemon.OK)),
	})
	if report.Daemon.OK {
		capResult, capErr := doctorDaemonAccountsCheck(ctx, opts.RequireSecretBackend)
		if capErr != "" {
			report.Codex.DaemonCheckError = capErr
			report.Issues = append(report.Issues, "daemon-side accounts/check failed")
			report.NextSteps = append(report.NextSteps, "inspect daemon-side account evidence with: "+doctorReadinessCommand)
		} else {
			report.Codex.DaemonCheckOK = true
			report.Codex.DaemonCheckedAccounts = capResult.CheckedAccounts
			report.Codex.DaemonSecretBackend = capResult.SecretBackend
		}
		evidence := "daemon accounts/check ok"
		if capErr != "" {
			evidence = capErr
		} else {
			evidence = fmt.Sprintf("checked %d via daemon, secret backend %s", capResult.CheckedAccounts, capResult.SecretBackend)
		}
		report.Checks = append(report.Checks, doctorCheckReport{
			Name:     "CAP accounts/check",
			OK:       capErr == "",
			Evidence: evidence,
			NextStep: doctorCheckNextStep(capErr != "", "inspect daemon-side account evidence with: "+doctorReadinessCommand),
		})
	}
	report.NextSteps = compactStrings(report.NextSteps)
	report.Issues = compactStrings(report.Issues)
	report.OK = len(report.Issues) == 0
	return report, nil
}

func doctorDaemonAccountsCheck(ctx context.Context, requireSecretBackend string) (protocol.AccountsCheckResult, string) {
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := daemonRPCCall(checkCtx, "capd-doctor", protocol.MethodAccountsCheck, protocol.AccountsCheckParams{
		Provider:             codexauth.Provider,
		RequireSecretBackend: requireSecretBackend,
	})
	if err != nil {
		return protocol.AccountsCheckResult{}, safeDoctorDaemonError(err)
	}
	var result protocol.AccountsCheckResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return protocol.AccountsCheckResult{}, "decode daemon accounts/check response failed"
	}
	return result, ""
}

func safeDoctorDaemonError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.Contains(msg, "no daemon token") {
		return "daemon token unavailable"
	}
	if strings.Contains(msg, "context deadline exceeded") {
		return "daemon accounts/check timed out"
	}
	return msg
}

func doctorBoolEvidence(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

func doctorCheckNextStep(include bool, step string) string {
	if include {
		return step
	}
	return ""
}

func doctorImportNextStep(daemonOK bool) string {
	if daemonOK {
		return "import a Codex account through CAP with: capd accounts import"
	}
	return "after starting the daemon, import through CAP with: capd accounts import (local fallback: capd accounts codex import)"
}

func doctorSecondImportNextStep(daemonOK bool) string {
	if daemonOK {
		return "import a second Codex account through CAP with: capd accounts import --auth /path/to/auth.json, then run: make live-codex-readiness"
	}
	return "start the daemon, import a second Codex account, then run: make live-codex-readiness"
}

func doctorAccountImportCheckNextStep(imported int, daemonOK bool) string {
	if imported == 0 {
		return doctorImportNextStep(daemonOK)
	}
	return doctorSecondImportNextStep(daemonOK)
}

func doctorQuotaCheckNextStep(imported int, daemonOK bool) string {
	if imported == 0 {
		return doctorImportNextStep(daemonOK)
	}
	return doctorReadinessNextStep()
}

func doctorRouteCheckNextStep(imported int, daemonOK bool) string {
	if imported == 0 {
		return doctorImportNextStep(daemonOK)
	}
	return doctorRouteReadinessNextStep()
}

func doctorReadinessNextStep() string {
	return "refresh and verify daemon-side readiness with: " + doctorReadinessCommand
}

func doctorRouteReadinessNextStep() string {
	return "refresh quota and verify routing with: " + doctorReadinessCommand
}

func printDoctorReport(cmd *cobra.Command, report doctorReport) {
	status := "ok"
	if !report.OK {
		status = "needs attention"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "capd doctor: %s\n", status)
	fmt.Fprintf(cmd.OutOrStdout(), "daemon: %s", report.Daemon.Addr)
	if report.Daemon.OK {
		fmt.Fprintln(cmd.OutOrStdout(), " ok")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), " failed: %s\n", report.Daemon.Error)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "codex: cli=%t accounts=%d current=%s secretBackend=%s freshQuota=%d staleQuota=%d missingQuota=%d\n",
		report.Codex.CLIAvailable,
		report.Codex.ImportedAccounts,
		emptyDash(report.Codex.CurrentAccountID),
		emptyDash(report.Codex.SecretBackend),
		report.Codex.FreshQuotaAccounts,
		report.Codex.StaleQuotaAccounts,
		report.Codex.MissingQuotaAccounts,
	)
	if report.Codex.AutoRouteAccountID != "" {
		primary := ""
		if report.Codex.AutoRoutePrimary != nil {
			primary = " primary=" + formatPercent(*report.Codex.AutoRoutePrimary)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "auto route: %s quota=%s fresh=%t score=%.2f%s %s\n",
			report.Codex.AutoRouteAccountID, report.Codex.AutoRouteQuotaState, report.Codex.AutoRouteFresh, report.Codex.AutoRouteScore, primary, report.Codex.AutoRouteReason)
	}
	if len(report.Codex.Accounts) > 0 {
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
		fmt.Fprintln(w, "CODEX_ACCOUNT\tCURRENT\tEMAIL\tPLAN\tQUOTA\tFRESH\tPRIMARY\tCHECKED_AT")
		for _, a := range report.Codex.Accounts {
			current := ""
			if a.Current {
				current = "*"
			}
			primary := ""
			if a.PrimaryUsedPercent != nil {
				primary = formatPercent(*a.PrimaryUsedPercent)
			}
			checkedAt := ""
			if a.QuotaCheckedAt > 0 {
				checkedAt = time.Unix(a.QuotaCheckedAt, 0).Format(time.RFC3339)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%t\t%s\t%s\n", a.ID, current, a.Email, a.Plan, a.QuotaState, a.QuotaFresh, primary, checkedAt)
		}
		_ = w.Flush()
	}
	if len(report.Checks) > 0 {
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
		fmt.Fprintln(w, "CHECK\tSTATUS\tEVIDENCE\tNEXT_STEP")
		for _, check := range report.Checks {
			status := "fail"
			if check.OK {
				status = "pass"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", check.Name, status, check.Evidence, check.NextStep)
		}
		_ = w.Flush()
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT\tSTATUS\tVERSION\tBIN")
	for _, a := range report.Agents {
		status := "not installed"
		if a.Available {
			status = "available"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.ID, status, a.Version, a.Bin)
	}
	_ = w.Flush()
	if len(report.Issues) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "issues:")
		for _, issue := range report.Issues {
			fmt.Fprintf(cmd.OutOrStdout(), "- %s\n", issue)
		}
	}
	if len(report.NextSteps) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "next steps:")
		for _, step := range report.NextSteps {
			fmt.Fprintf(cmd.OutOrStdout(), "- %s\n", step)
		}
	}
}

func compactStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func emptyDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
