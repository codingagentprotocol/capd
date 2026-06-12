package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/internal/account/codexquota"
	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/daemon"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func newAccountsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "accounts",
		Short: "Manage local agent accounts",
	}
	cmd.PersistentFlags().String("secret-backend", "", "secret storage backend for account token material (file or native; default CAPD_SECRET_BACKEND/file)")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List imported accounts across all providers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			accounts, _, err := accountDepsFromCmd(cmd)
			if err != nil {
				return err
			}
			defer accounts.Close()
			list, err := accounts.ListAccounts("")
			if err != nil {
				return err
			}
			rows := make([]accountListRow, 0, len(list))
			currentByProvider := map[string]string{}
			for _, acc := range list {
				current, ok := currentByProvider[acc.Provider]
				if !ok {
					current, err = accounts.CurrentAccount(acc.Provider)
					if err != nil {
						return err
					}
					currentByProvider[acc.Provider] = current
				}
				row := accountListRow{
					Current:    acc.ID == current,
					ID:         acc.ID,
					Provider:   acc.Provider,
					AuthMode:   acc.AuthMode,
					Email:      acc.Email,
					AccountID:  acc.AccountID,
					Plan:       acc.Plan,
					QuotaState: protocol.AccountQuotaStateMissing,
				}
				if q, err := accounts.LoadQuota(acc.ID); err == nil {
					if row.Plan == "" {
						row.Plan = q.Plan
					}
					row.PrimaryUsed = formatPercent(q.PrimaryUsedPercent)
					row.QuotaState = accountQuotaState(q)
					row.QuotaCheckedAt = q.CheckedAt
				}
				rows = append(rows, row)
			}
			if jsonOut {
				out, _ := json.MarshalIndent(rows, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "CURRENT\tPROVIDER\tID\tMODE\tEMAIL\tREMOTE_ACCOUNT\tPLAN\tPRIMARY_USED\tQUOTA_STATE")
			for _, row := range rows {
				mark := ""
				if row.Current {
					mark = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					mark, row.Provider, row.ID, row.AuthMode, row.Email, row.AccountID, row.Plan, row.PrimaryUsed, row.QuotaState)
			}
			return w.Flush()
		},
	}
	listCmd.Flags().Bool("json", false, "print imported account metadata as JSON without token material")

	checkCmd := &cobra.Command{
		Use:   "check",
		Short: "Run daemon-side account smoke checks through CAP",
		Long:  "Run the daemon-side accounts/check RPC and print safe account smoke evidence without token material or runtime paths.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			provider, _ := cmd.Flags().GetString("provider")
			jsonOut, _ := cmd.Flags().GetBool("json")
			requireMultiple, _ := cmd.Flags().GetBool("require-multiple")
			requireFreshQuota, _ := cmd.Flags().GetBool("require-fresh-quota")
			requireAllFreshQuota, _ := cmd.Flags().GetBool("require-all-fresh-quota")
			requireSecretBackend, _ := cmd.Flags().GetString("require-secret-backend")
			raw, err := daemonRPCCall(cmd.Context(), "capd-accounts-check", protocol.MethodAccountsCheck, protocol.AccountsCheckParams{Provider: provider})
			if err != nil {
				return err
			}
			var result protocol.AccountsCheckResult
			if err := json.Unmarshal(raw, &result); err != nil {
				return err
			}
			if requireSecretBackend != "" && result.SecretBackend != requireSecretBackend {
				return fmt.Errorf("secret backend = %q, want %q", result.SecretBackend, requireSecretBackend)
			}
			if requireMultiple && result.CheckedAccounts < 2 {
				return fmt.Errorf("expected multiple Codex accounts, found %d", result.CheckedAccounts)
			}
			if requireFreshQuota && (result.AutoRoute == nil || !result.AutoRoute.Fresh) {
				return fmt.Errorf("auto route does not have fresh cached quota; refresh quota first")
			}
			if requireAllFreshQuota {
				for _, row := range result.Accounts {
					if !row.QuotaFresh {
						return fmt.Errorf("%s: quota is %s; refresh every account first", row.ID, row.QuotaState)
					}
				}
			}
			if jsonOut {
				out, _ := json.MarshalIndent(result, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "provider: %s\n", result.Provider)
			if result.CurrentAccountID != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "current: %s\n", result.CurrentAccountID)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "secret backend: %s\n", result.SecretBackend)
			if result.AutoRoute != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "auto route: %s score %.2f quota %s\n", result.AutoRoute.AccountID, result.AutoRoute.Score, result.AutoRoute.QuotaState)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "CURRENT\tID\tEMAIL\tSECRET\tCREDENTIAL\tRUNTIME\tAUTH_JSON\tMARKER\tQUOTA")
			for _, row := range result.Accounts {
				mark := ""
				if row.Current {
					mark = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%t\t%t\t%t\t%t\t%s\n",
					mark, row.ID, row.Email, row.SecretBackendOK, row.CredentialReadable, row.RuntimeReady, row.AuthJSONPrivate, row.ProjectionMarkerOK, row.QuotaState)
			}
			return w.Flush()
		},
	}
	checkCmd.Flags().String("provider", "codex", "account provider to check")
	checkCmd.Flags().Bool("require-multiple", false, "fail unless at least two accounts are checked")
	checkCmd.Flags().Bool("require-fresh-quota", false, "fail unless auto-route selection is backed by fresh cached quota")
	checkCmd.Flags().Bool("require-all-fresh-quota", false, "fail unless every checked account has fresh cached quota")
	checkCmd.Flags().String("require-secret-backend", "", "fail unless daemon account check uses this SecretStore backend")
	checkCmd.Flags().Bool("json", false, "print accounts/check result as JSON without token material")

	cmd.AddCommand(listCmd, checkCmd, newCodexAccountsCmd())
	return cmd
}

func newCodexAccountsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "codex",
		Short: "Manage Codex accounts imported into capd",
	}

	importCmd := &cobra.Command{
		Use:   "import",
		Short: "Import the local Codex auth.json into capd",
		RunE: func(cmd *cobra.Command, _ []string) error {
			authPath, _ := cmd.Flags().GetString("auth")
			if authPath == "" {
				var err error
				authPath, err = codexauth.DefaultAuthPath("")
				if err != nil {
					return err
				}
			}
			accounts, secrets, err := accountDepsFromCmd(cmd)
			if err != nil {
				return err
			}
			defer accounts.Close()
			result, err := codexauth.Importer{Accounts: accounts, Secrets: secrets}.ImportAuthJSON(cmd.Context(), authPath)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "imported %s", result.Account.ID)
			if result.Account.Email != "" {
				fmt.Fprintf(cmd.OutOrStdout(), " <%s>", result.Account.Email)
			}
			fmt.Fprintln(cmd.OutOrStdout())
			return nil
		},
	}
	importCmd.Flags().String("auth", "", "path to Codex auth.json (default: ~/.codex/auth.json)")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List Codex accounts imported into capd",
		RunE: func(cmd *cobra.Command, _ []string) error {
			accounts, _, err := accountDepsFromCmd(cmd)
			if err != nil {
				return err
			}
			defer accounts.Close()
			current, err := accounts.CurrentAccount(codexauth.Provider)
			if err != nil {
				return err
			}
			list, err := accounts.ListAccounts(codexauth.Provider)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "CURRENT\tID\tMODE\tEMAIL\tCHATGPT_ACCOUNT\tPLAN\tPRIMARY_USED\tQUOTA_STATE")
			for _, acc := range list {
				mark := ""
				if acc.ID == current {
					mark = "*"
				}
				plan := acc.Plan
				used := ""
				quotaState := protocol.AccountQuotaStateMissing
				if q, err := accounts.LoadQuota(acc.ID); err == nil {
					if plan == "" {
						plan = q.Plan
					}
					used = formatPercent(q.PrimaryUsedPercent)
					quotaState = accountQuotaState(q)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", mark, acc.ID, acc.AuthMode, acc.Email, acc.AccountID, plan, used, quotaState)
			}
			return w.Flush()
		},
	}

	currentCmd := &cobra.Command{
		Use:   "current [account-id]",
		Short: "Show or set the current Codex account",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			accounts, _, err := accountDepsFromCmd(cmd)
			if err != nil {
				return err
			}
			defer accounts.Close()
			if len(args) == 1 {
				if _, err := accounts.LoadAccount(args[0]); err != nil {
					return err
				}
				if err := accounts.SetCurrentAccount(codexauth.Provider, args[0]); err != nil {
					return err
				}
			}
			current, err := accounts.CurrentAccount(codexauth.Provider)
			if err != nil {
				return err
			}
			if current == "" {
				return fmt.Errorf("no current Codex account")
			}
			fmt.Fprintln(cmd.OutOrStdout(), current)
			return nil
		},
	}

	projectCmd := &cobra.Command{
		Use:   "project [account-id]",
		Short: "Create or refresh a capd-managed Codex CODEX_HOME projection",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			accounts, secrets, err := accountDepsFromCmd(cmd)
			if err != nil {
				return err
			}
			defer accounts.Close()
			id := ""
			if len(args) == 1 {
				id = args[0]
			} else {
				id, err = accounts.CurrentAccount(codexauth.Provider)
				if err != nil {
					return err
				}
			}
			if id == "" {
				return fmt.Errorf("no Codex account selected")
			}
			acc, err := accounts.LoadAccount(id)
			if err != nil {
				return err
			}
			home, err := daemon.Home()
			if err != nil {
				return err
			}
			profile, err := codexauth.RuntimeProjector{
				Root:    filepath.Join(home, "runtimes"),
				Secrets: secrets,
			}.Project(cmd.Context(), acc)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), profile.CodexHome)
			return nil
		},
	}

	removeCmd := &cobra.Command{
		Use:   "remove <account-id>",
		Short: "Remove an imported Codex account and its stored token material",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			accounts, secrets, err := accountDepsFromCmd(cmd)
			if err != nil {
				return err
			}
			defer accounts.Close()
			acc, err := accounts.LoadAccount(args[0])
			if err != nil {
				return err
			}
			if acc.Provider != codexauth.Provider {
				return fmt.Errorf("account %q belongs to provider %q, not %q", acc.ID, acc.Provider, codexauth.Provider)
			}
			ref, err := secret.ParseRef(acc.SecretRef)
			if err != nil {
				return err
			}
			if err := secret.EnsureRefBackend(secrets, ref); err != nil {
				return err
			}
			home, err := daemon.Home()
			if err != nil {
				return err
			}
			if _, err := codexauth.RemoveRuntimeProjection(filepath.Join(home, "runtimes"), acc); err != nil {
				return err
			}
			if err := secrets.Delete(cmd.Context(), ref); err != nil {
				return err
			}
			if err := accounts.DeleteAccount(acc.ID); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", acc.ID)
			return nil
		},
	}

	quotaCmd := &cobra.Command{
		Use:   "quota [account-id|auto|all]",
		Short: "Fetch ChatGPT backend quota for imported Codex accounts",
		Long:  "Fetch ChatGPT backend quota for imported Codex accounts. With auto, capd uses the same conservative quota scoring rule as account-aware routing. With all, capd refreshes every imported Codex account and prints safe summaries.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseURL, _ := cmd.Flags().GetString("base-url")
			rawOut, _ := cmd.Flags().GetBool("raw")
			accounts, secrets, err := accountDepsFromCmd(cmd)
			if err != nil {
				return err
			}
			defer accounts.Close()
			id := ""
			if len(args) == 1 {
				id = args[0]
			} else {
				id, err = accounts.CurrentAccount(codexauth.Provider)
				if err != nil {
					return err
				}
			}
			if id == "" {
				return fmt.Errorf("no Codex account selected")
			}
			if id == protocol.AccountAll {
				if rawOut {
					return fmt.Errorf("--raw is only supported for a single account")
				}
				list, err := accounts.ListAccounts(codexauth.Provider)
				if err != nil {
					return err
				}
				if len(list) == 0 {
					return fmt.Errorf("no imported Codex accounts; run capd accounts codex import first")
				}
				rows := make([]codexQuotaSummary, 0, len(list))
				for _, acc := range list {
					row, err := refreshCodexQuota(cmd.Context(), accounts, secrets, baseURL, acc)
					if err != nil {
						return fmt.Errorf("%s: %w", acc.ID, err)
					}
					rows = append(rows, row)
				}
				out, _ := json.MarshalIndent(rows, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			acc, err := resolveUsageAccount(accounts, id)
			if err != nil {
				return err
			}
			summary, usage, err := refreshCodexQuotaWithUsage(cmd.Context(), accounts, secrets, baseURL, acc)
			if err != nil {
				return err
			}
			var body any = summary
			if rawOut {
				body = usage
			}
			out, _ := json.MarshalIndent(body, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}
	quotaCmd.Flags().String("base-url", "", "override ChatGPT base URL for testing")
	quotaCmd.Flags().Bool("raw", false, "print raw backend usage JSON for debugging")

	smokeCmd := &cobra.Command{
		Use:   "smoke",
		Short: "Run a safe local smoke check for imported Codex accounts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			refreshQuota, _ := cmd.Flags().GetBool("quota")
			baseURL, _ := cmd.Flags().GetString("base-url")
			requireMultiple, _ := cmd.Flags().GetBool("require-multiple")
			requireFreshQuota, _ := cmd.Flags().GetBool("require-fresh-quota")
			requireAllFreshQuota, _ := cmd.Flags().GetBool("require-all-fresh-quota")
			requireSecretBackend, _ := cmd.Flags().GetString("require-secret-backend")
			jsonOut, _ := cmd.Flags().GetBool("json")
			accounts, secrets, err := accountDepsFromCmd(cmd)
			if err != nil {
				return err
			}
			defer accounts.Close()
			list, err := accounts.ListAccounts(codexauth.Provider)
			if err != nil {
				return err
			}
			if len(list) == 0 {
				return fmt.Errorf("no imported Codex accounts; run capd accounts codex import first")
			}
			if requireMultiple && len(list) < 2 {
				return fmt.Errorf("expected multiple Codex accounts, found %d", len(list))
			}
			home, err := daemon.Home()
			if err != nil {
				return err
			}
			result := codexSmokeResult{
				OK:              true,
				CheckedAccounts: len(list),
				QuotaRefreshed:  refreshQuota,
				SecretBackend:   secrets.Backend(),
				Accounts:        make([]codexSmokeAccount, 0, len(list)),
			}
			if requireSecretBackend != "" && requireSecretBackend != result.SecretBackend {
				return fmt.Errorf("secret backend = %q, want %q", result.SecretBackend, requireSecretBackend)
			}
			for _, acc := range list {
				ref, err := secret.ParseRef(acc.SecretRef)
				if err != nil {
					return fmt.Errorf("%s: parse secret ref: %w", acc.ID, err)
				}
				if err := secret.EnsureRefBackend(secrets, ref); err != nil {
					return fmt.Errorf("%s: %w", acc.ID, err)
				}
				bundle, err := secrets.Get(cmd.Context(), ref)
				if err != nil {
					return fmt.Errorf("%s: load secret: %w", acc.ID, err)
				}
				profile, err := codexauth.RuntimeProjector{
					Root:    filepath.Join(home, "runtimes"),
					Secrets: secrets,
				}.Project(cmd.Context(), acc)
				if err != nil {
					return fmt.Errorf("%s: project runtime: %w", acc.ID, err)
				}
				projection, err := verifyProjectedRuntime(profile)
				if err != nil {
					return fmt.Errorf("%s: verify projection: %w", acc.ID, err)
				}
				row := codexSmokeAccount{
					ID:                 acc.ID,
					Email:              acc.Email,
					AuthMode:           acc.AuthMode,
					ProjectedCodexHome: profile.CodexHome,
					ProjectionOK:       true,
					RuntimeEnvOK:       projection.RuntimeEnvOK,
					AuthJSONPrivate:    projection.AuthJSONPrivate,
					ProjectionMarkerOK: projection.ProjectionMarkerOK,
					SecretBackendOK:    true,
					SecretReadable:     true,
				}
				if refreshQuota {
					if bundle.AccountID == "" {
						bundle.AccountID = acc.AccountID
					}
					quotaResult, err := codexquota.Client{BaseURL: baseURL}.Usage(cmd.Context(), acc.ID, bundle)
					if err != nil {
						return fmt.Errorf("%s: refresh quota: %w", acc.ID, err)
					}
					if err := accounts.SaveQuota(quotaResult.Quota); err != nil {
						return fmt.Errorf("%s: save quota: %w", acc.ID, err)
					}
					setCodexSmokeQuotaEvidence(&row, quotaResult.Quota)
				} else if q, err := accounts.LoadQuota(acc.ID); err == nil {
					setCodexSmokeQuotaEvidence(&row, q)
				} else {
					row.PrimaryUsed = "cached-missing"
					row.QuotaState = protocol.AccountQuotaStateMissing
				}
				if requireAllFreshQuota && !row.QuotaFresh {
					return fmt.Errorf("%s: quota is %s; run with --quota or refresh every account first", acc.ID, row.QuotaState)
				}
				result.Accounts = append(result.Accounts, row)
			}
			if route, err := codexSmokeAutoRouteEvidence(accounts); err != nil {
				return err
			} else {
				result.AutoRoute = route
			}
			if requireFreshQuota && (result.AutoRoute == nil || !result.AutoRoute.Fresh) {
				return fmt.Errorf("auto route does not have fresh cached quota; run with --quota or refresh quota first")
			}
			if jsonOut {
				out, _ := json.MarshalIndent(result, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tEMAIL\tPROJECTED_CODEX_HOME\tAUTH_MODE\tPRIMARY_USED")
			for _, row := range result.Accounts {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", row.ID, row.Email, row.ProjectedCodexHome, row.AuthMode, row.PrimaryUsed)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if result.AutoRoute != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "auto route: %s (%s)\n", result.AutoRoute.AccountID, result.AutoRoute.Reason)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "secret backend: %s\n", result.SecretBackend)
			return nil
		},
	}
	smokeCmd.Flags().Bool("quota", false, "also refresh ChatGPT backend quota for every imported account")
	smokeCmd.Flags().String("base-url", "", "override ChatGPT base URL for testing")
	smokeCmd.Flags().Bool("require-multiple", false, "fail unless at least two Codex accounts are imported")
	smokeCmd.Flags().Bool("require-fresh-quota", false, "fail unless auto-route selection is backed by fresh cached quota")
	smokeCmd.Flags().Bool("require-all-fresh-quota", false, "fail unless every imported account has fresh cached quota")
	smokeCmd.Flags().String("require-secret-backend", "", "fail unless the active secret backend matches this value")
	smokeCmd.Flags().Bool("json", false, "print machine-readable smoke evidence without token material")

	cmd.AddCommand(importCmd, listCmd, currentCmd, projectCmd, removeCmd, quotaCmd, smokeCmd)
	return cmd
}

func verifyProjectedAuth(codexHome string) error {
	evidence, err := codexauth.VerifyRuntimeProfile(codexauth.RuntimeProfile{
		AccountID: filepath.Base(codexHome),
		CodexHome: codexHome,
		Env:       []string{"CODEX_HOME=" + codexHome},
	})
	if err != nil {
		return err
	}
	if !evidence.AuthJSONPrivate {
		return fmt.Errorf("auth.json is not private")
	}
	return nil
}

func verifyProjectedRuntime(profile codexauth.RuntimeProfile) (codexSmokeProjection, error) {
	evidence, err := codexauth.VerifyRuntimeProfile(profile)
	if err != nil {
		return codexSmokeProjection{}, err
	}
	return codexSmokeProjection{
		RuntimeEnvOK:       evidence.RuntimeEnvOK,
		AuthJSONPrivate:    evidence.AuthJSONPrivate,
		ProjectionMarkerOK: evidence.ProjectionMarkerOK,
	}, nil
}

type codexSmokeResult struct {
	OK              bool                 `json:"ok"`
	CheckedAccounts int                  `json:"checkedAccounts"`
	QuotaRefreshed  bool                 `json:"quotaRefreshed"`
	SecretBackend   string               `json:"secretBackend"`
	AutoRoute       *codexSmokeAutoRoute `json:"autoRoute,omitempty"`
	Accounts        []codexSmokeAccount  `json:"accounts"`
}

type codexSmokeAutoRoute struct {
	AccountID  string   `json:"accountId"`
	Reason     string   `json:"reason"`
	Score      float64  `json:"score"`
	QuotaState string   `json:"quotaState"`
	Fresh      bool     `json:"fresh"`
	CheckedAt  int64    `json:"checkedAt,omitempty"`
	Primary    *float64 `json:"primaryUsedPercent,omitempty"`
}

type codexSmokeAccount struct {
	ID                 string   `json:"id"`
	Email              string   `json:"email,omitempty"`
	AuthMode           string   `json:"authMode,omitempty"`
	ProjectedCodexHome string   `json:"projectedCodexHome"`
	ProjectionOK       bool     `json:"projectionOk"`
	RuntimeEnvOK       bool     `json:"runtimeEnvOk"`
	AuthJSONPrivate    bool     `json:"authJsonPrivate"`
	ProjectionMarkerOK bool     `json:"projectionMarkerOk"`
	SecretBackendOK    bool     `json:"secretBackendOk"`
	SecretReadable     bool     `json:"secretReadable"`
	PrimaryUsed        string   `json:"primaryUsed"`
	PrimaryUsedPercent *float64 `json:"primaryUsedPercent,omitempty"`
	QuotaState         string   `json:"quotaState"`
	QuotaFresh         bool     `json:"quotaFresh"`
	QuotaCheckedAt     int64    `json:"quotaCheckedAt,omitempty"`
}

type codexQuotaSummary struct {
	ID                    string  `json:"id"`
	Provider              string  `json:"provider"`
	Email                 string  `json:"email,omitempty"`
	AccountID             string  `json:"accountId,omitempty"`
	Plan                  string  `json:"plan,omitempty"`
	PrimaryUsedPercent    float64 `json:"primaryUsedPercent"`
	PrimaryResetAt        string  `json:"primaryResetAt,omitempty"`
	SecondaryUsedPercent  float64 `json:"secondaryUsedPercent"`
	SecondaryResetAt      string  `json:"secondaryResetAt,omitempty"`
	CodeReviewUsedPercent float64 `json:"codeReviewUsedPercent"`
	CheckedAt             int64   `json:"checkedAt"`
	QuotaState            string  `json:"quotaState"`
}

func codexQuotaSummaryFrom(acc account.Account, q account.QuotaSnapshot) codexQuotaSummary {
	plan := acc.Plan
	if plan == "" {
		plan = q.Plan
	}
	return codexQuotaSummary{
		ID:                    acc.ID,
		Provider:              acc.Provider,
		Email:                 acc.Email,
		AccountID:             acc.AccountID,
		Plan:                  plan,
		PrimaryUsedPercent:    q.PrimaryUsedPercent,
		PrimaryResetAt:        q.PrimaryResetAt,
		SecondaryUsedPercent:  q.SecondaryUsedPercent,
		SecondaryResetAt:      q.SecondaryResetAt,
		CodeReviewUsedPercent: q.CodeReviewUsedPercent,
		CheckedAt:             q.CheckedAt,
		QuotaState:            accountQuotaState(q),
	}
}

func refreshCodexQuota(ctx context.Context, accounts *account.Store, secrets secret.Store, baseURL string, acc account.Account) (codexQuotaSummary, error) {
	summary, _, err := refreshCodexQuotaWithUsage(ctx, accounts, secrets, baseURL, acc)
	return summary, err
}

func refreshCodexQuotaWithUsage(ctx context.Context, accounts *account.Store, secrets secret.Store, baseURL string, acc account.Account) (codexQuotaSummary, map[string]any, error) {
	ref, err := secret.ParseRef(acc.SecretRef)
	if err != nil {
		return codexQuotaSummary{}, nil, err
	}
	if err := secret.EnsureRefBackend(secrets, ref); err != nil {
		return codexQuotaSummary{}, nil, err
	}
	bundle, err := secrets.Get(ctx, ref)
	if err != nil {
		return codexQuotaSummary{}, nil, err
	}
	if bundle.AccountID == "" {
		bundle.AccountID = acc.AccountID
	}
	result, err := codexquota.Client{BaseURL: baseURL}.Usage(ctx, acc.ID, bundle)
	if err != nil {
		return codexQuotaSummary{}, nil, err
	}
	if err := accounts.SaveQuota(result.Quota); err != nil {
		return codexQuotaSummary{}, nil, err
	}
	return codexQuotaSummaryFrom(acc, result.Quota), result.Usage, nil
}

type codexSmokeProjection struct {
	RuntimeEnvOK       bool
	AuthJSONPrivate    bool
	ProjectionMarkerOK bool
}

type accountListRow struct {
	Current        bool   `json:"current"`
	Provider       string `json:"provider"`
	ID             string `json:"id"`
	AuthMode       string `json:"authMode,omitempty"`
	Email          string `json:"email,omitempty"`
	AccountID      string `json:"accountId,omitempty"`
	Plan           string `json:"plan,omitempty"`
	PrimaryUsed    string `json:"primaryUsed,omitempty"`
	QuotaState     string `json:"quotaState"`
	QuotaCheckedAt int64  `json:"quotaCheckedAt,omitempty"`
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

func setCodexSmokeQuotaEvidence(row *codexSmokeAccount, q account.QuotaSnapshot) {
	row.PrimaryUsedPercent = &q.PrimaryUsedPercent
	row.PrimaryUsed = formatPercent(q.PrimaryUsedPercent)
	row.QuotaCheckedAt = q.CheckedAt
	row.QuotaState = accountQuotaState(q)
	if row.QuotaState == protocol.AccountQuotaStateFresh {
		row.QuotaFresh = true
		return
	}
}

func codexSmokeAutoRouteEvidence(accounts *account.Store) (*codexSmokeAutoRoute, error) {
	acc, err := account.SelectQuotaRouteAccount(accounts, codexauth.Provider)
	if err != nil {
		return nil, err
	}
	current, _ := accounts.CurrentAccount(codexauth.Provider)
	route := &codexSmokeAutoRoute{
		AccountID:  acc.ID,
		Score:      account.QuotaRouteScore(accounts, acc, current),
		QuotaState: protocol.AccountQuotaStateMissing,
	}
	if q, err := accounts.LoadQuota(acc.ID); err == nil {
		route.CheckedAt = q.CheckedAt
		route.Primary = &q.PrimaryUsedPercent
		if account.QuotaSnapshotFresh(q, time.Now()) {
			route.QuotaState = protocol.AccountQuotaStateFresh
			route.Fresh = true
			route.Reason = fmt.Sprintf("fresh primary quota %.1f%%", q.PrimaryUsedPercent)
		} else {
			route.QuotaState = protocol.AccountQuotaStateStale
			route.Reason = "conservative score with stale cached quota"
		}
	} else {
		route.Reason = "conservative score without fresh cached quota"
	}
	return route, nil
}

func formatPercent(value float64) string {
	return fmt.Sprintf("%.1f%%", value)
}

func openAccountDeps() (*account.Store, secret.Store, error) {
	return openAccountDepsWithBackend("")
}

func accountDepsFromCmd(cmd *cobra.Command) (*account.Store, secret.Store, error) {
	backend := accountSecretBackendFlag(cmd)
	return openAccountDepsWithBackend(backend)
}

func accountSecretBackendFlag(cmd *cobra.Command) string {
	for _, flags := range []*pflag.FlagSet{cmd.Flags(), cmd.InheritedFlags(), cmd.Root().PersistentFlags()} {
		if flags == nil {
			continue
		}
		if f := flags.Lookup("secret-backend"); f != nil {
			return f.Value.String()
		}
	}
	return ""
}

func openAccountDepsWithBackend(backend string) (*account.Store, secret.Store, error) {
	home, err := daemon.Home()
	if err != nil {
		return nil, nil, err
	}
	accounts, err := account.OpenStore(filepath.Join(home, "accounts.db"))
	if err != nil {
		return nil, nil, err
	}
	secrets, err := secret.Open(filepath.Join(home, "secrets", "codex"), backend)
	if err != nil {
		accounts.Close()
		return nil, nil, err
	}
	return accounts, secrets, nil
}
