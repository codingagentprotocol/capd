package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/internal/account/codexquota"
	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/daemon"
)

func newAccountsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "accounts",
		Short: "Manage local agent accounts",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List imported accounts across all providers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			accounts, _, err := openAccountDeps()
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
					Current:   acc.ID == current,
					ID:        acc.ID,
					Provider:  acc.Provider,
					AuthMode:  acc.AuthMode,
					Email:     acc.Email,
					AccountID: acc.AccountID,
					Plan:      acc.Plan,
				}
				if q, err := accounts.LoadQuota(acc.ID); err == nil {
					if row.Plan == "" {
						row.Plan = q.Plan
					}
					row.PrimaryUsed = formatPercent(q.PrimaryUsedPercent)
				}
				rows = append(rows, row)
			}
			if jsonOut {
				out, _ := json.MarshalIndent(rows, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "CURRENT\tPROVIDER\tID\tMODE\tEMAIL\tREMOTE_ACCOUNT\tPLAN\tPRIMARY_USED")
			for _, row := range rows {
				mark := ""
				if row.Current {
					mark = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					mark, row.Provider, row.ID, row.AuthMode, row.Email, row.AccountID, row.Plan, row.PrimaryUsed)
			}
			return w.Flush()
		},
	}
	listCmd.Flags().Bool("json", false, "print imported account metadata as JSON without token material")

	cmd.AddCommand(listCmd, newCodexAccountsCmd())
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
			accounts, secrets, err := openAccountDeps()
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
			accounts, _, err := openAccountDeps()
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
			fmt.Fprintln(w, "CURRENT\tID\tMODE\tEMAIL\tCHATGPT_ACCOUNT\tPLAN\tPRIMARY_USED")
			for _, acc := range list {
				mark := ""
				if acc.ID == current {
					mark = "*"
				}
				plan := acc.Plan
				used := ""
				if q, err := accounts.LoadQuota(acc.ID); err == nil {
					if plan == "" {
						plan = q.Plan
					}
					used = formatPercent(q.PrimaryUsedPercent)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", mark, acc.ID, acc.AuthMode, acc.Email, acc.AccountID, plan, used)
			}
			return w.Flush()
		},
	}

	currentCmd := &cobra.Command{
		Use:   "current [account-id]",
		Short: "Show or set the current Codex account",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			accounts, _, err := openAccountDeps()
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
			accounts, secrets, err := openAccountDeps()
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

	quotaCmd := &cobra.Command{
		Use:   "quota [account-id]",
		Short: "Fetch ChatGPT backend quota for an imported Codex account",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseURL, _ := cmd.Flags().GetString("base-url")
			rawOut, _ := cmd.Flags().GetBool("raw")
			accounts, secrets, err := openAccountDeps()
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
			ref, err := secret.ParseRef(acc.SecretRef)
			if err != nil {
				return err
			}
			bundle, err := secrets.Get(cmd.Context(), ref)
			if err != nil {
				return err
			}
			result, err := codexquota.Client{BaseURL: baseURL}.Usage(cmd.Context(), acc.ID, bundle)
			if err != nil {
				return err
			}
			if err := accounts.SaveQuota(result.Quota); err != nil {
				return err
			}
			var body any = codexQuotaSummaryFrom(acc, result.Quota)
			if rawOut {
				body = result.Usage
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
			requireSecretBackend, _ := cmd.Flags().GetString("require-secret-backend")
			jsonOut, _ := cmd.Flags().GetBool("json")
			accounts, secrets, err := openAccountDeps()
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
				}
				used := "cached-missing"
				if refreshQuota {
					quotaResult, err := codexquota.Client{BaseURL: baseURL}.Usage(cmd.Context(), acc.ID, bundle)
					if err != nil {
						return fmt.Errorf("%s: refresh quota: %w", acc.ID, err)
					}
					if err := accounts.SaveQuota(quotaResult.Quota); err != nil {
						return fmt.Errorf("%s: save quota: %w", acc.ID, err)
					}
					row.PrimaryUsedPercent = &quotaResult.Quota.PrimaryUsedPercent
					used = formatPercent(quotaResult.Quota.PrimaryUsedPercent)
				} else if q, err := accounts.LoadQuota(acc.ID); err == nil {
					row.PrimaryUsedPercent = &q.PrimaryUsedPercent
					used = formatPercent(q.PrimaryUsedPercent)
				}
				row.PrimaryUsed = used
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
	smokeCmd.Flags().String("require-secret-backend", "", "fail unless the active secret backend matches this value")
	smokeCmd.Flags().Bool("json", false, "print machine-readable smoke evidence without token material")

	cmd.AddCommand(importCmd, listCmd, currentCmd, projectCmd, quotaCmd, smokeCmd)
	return cmd
}

func verifyProjectedAuth(codexHome string) error {
	info, err := os.Stat(filepath.Join(codexHome, "auth.json"))
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("auth.json mode = %o, want 600", info.Mode().Perm())
	}
	return nil
}

func verifyProjectedRuntime(profile codexauth.RuntimeProfile) (codexSmokeProjection, error) {
	if profile.CodexHome == "" {
		return codexSmokeProjection{}, fmt.Errorf("CODEX_HOME projection path is empty")
	}
	wantEnv := "CODEX_HOME=" + profile.CodexHome
	envOK := false
	for _, entry := range profile.Env {
		if entry == wantEnv {
			envOK = true
			break
		}
	}
	if !envOK {
		return codexSmokeProjection{}, fmt.Errorf("runtime env missing %s", wantEnv)
	}
	if err := verifyProjectedAuth(profile.CodexHome); err != nil {
		return codexSmokeProjection{}, err
	}
	markerPath := filepath.Join(profile.CodexHome, ".capd_projection.json")
	info, err := os.Stat(markerPath)
	if err != nil {
		return codexSmokeProjection{}, err
	}
	if info.Mode().Perm() != 0o600 {
		return codexSmokeProjection{}, fmt.Errorf(".capd_projection.json mode = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return codexSmokeProjection{}, err
	}
	var marker struct {
		ManagedBy string `json:"managedBy"`
		Provider  string `json:"provider"`
		Account   string `json:"account"`
	}
	if err := json.Unmarshal(data, &marker); err != nil {
		return codexSmokeProjection{}, err
	}
	if marker.ManagedBy != "capd" || marker.Provider != codexauth.Provider || marker.Account != profile.AccountID {
		return codexSmokeProjection{}, fmt.Errorf("projection marker mismatch")
	}
	return codexSmokeProjection{
		RuntimeEnvOK:       true,
		AuthJSONPrivate:    true,
		ProjectionMarkerOK: true,
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
	AccountID string   `json:"accountId"`
	Reason    string   `json:"reason"`
	Score     float64  `json:"score"`
	Fresh     bool     `json:"fresh"`
	Primary   *float64 `json:"primaryUsedPercent,omitempty"`
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
	PrimaryUsed        string   `json:"primaryUsed"`
	PrimaryUsedPercent *float64 `json:"primaryUsedPercent,omitempty"`
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
	}
}

type codexSmokeProjection struct {
	RuntimeEnvOK       bool
	AuthJSONPrivate    bool
	ProjectionMarkerOK bool
}

type accountListRow struct {
	Current     bool   `json:"current"`
	Provider    string `json:"provider"`
	ID          string `json:"id"`
	AuthMode    string `json:"authMode,omitempty"`
	Email       string `json:"email,omitempty"`
	AccountID   string `json:"accountId,omitempty"`
	Plan        string `json:"plan,omitempty"`
	PrimaryUsed string `json:"primaryUsed,omitempty"`
}

func codexSmokeAutoRouteEvidence(accounts *account.Store) (*codexSmokeAutoRoute, error) {
	acc, err := account.SelectLowestQuotaAccount(accounts, codexauth.Provider)
	if err != nil {
		return nil, err
	}
	current, _ := accounts.CurrentAccount(codexauth.Provider)
	route := &codexSmokeAutoRoute{
		AccountID: acc.ID,
		Score:     account.QuotaRouteScore(accounts, acc, current),
	}
	if q, err := accounts.LoadQuota(acc.ID); err == nil && account.QuotaSnapshotFresh(q, time.Now()) {
		route.Fresh = true
		route.Primary = &q.PrimaryUsedPercent
		route.Reason = fmt.Sprintf("lowest fresh cached primary quota %.1f%%", q.PrimaryUsedPercent)
	} else {
		route.Reason = "selected without fresh cached quota"
	}
	return route, nil
}

func formatPercent(value float64) string {
	return fmt.Sprintf("%.1f%%", value)
}

func openAccountDeps() (*account.Store, secret.Store, error) {
	home, err := daemon.Home()
	if err != nil {
		return nil, nil, err
	}
	accounts, err := account.OpenStore(filepath.Join(home, "accounts.db"))
	if err != nil {
		return nil, nil, err
	}
	secrets, err := secret.Open(filepath.Join(home, "secrets", "codex"), "")
	if err != nil {
		accounts.Close()
		return nil, nil, err
	}
	return accounts, secrets, nil
}
