package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
			sort.Slice(list, func(i, j int) bool {
				if list[i].Provider != list[j].Provider {
					return list[i].Provider < list[j].Provider
				}
				return list[i].ID < list[j].ID
			})
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
				row := makeAccountListRow(accounts, acc, current)
				rows = append(rows, row)
			}
			if jsonOut {
				out, _ := json.MarshalIndent(rows, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "CURRENT\tPROVIDER\tID\tMODE\tEMAIL\tREMOTE_ACCOUNT\tPLAN\tSECRET_BACKEND\tPRIMARY_USED\tQUOTA_STATE\tFRESH\tROUTE_SCORE")
			for _, row := range rows {
				mark := ""
				if row.Current {
					mark = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%t\t%s\n",
					mark, row.Provider, row.ID, row.AuthMode, row.Email, row.AccountID, row.Plan, row.SecretBackend, row.PrimaryUsed, row.QuotaState, row.QuotaFresh, routeScoreText(row.RouteScore))
			}
			return w.Flush()
		},
	}
	listCmd.Flags().Bool("json", false, "print imported account metadata as JSON without token material")

	importCmd := &cobra.Command{
		Use:   "import",
		Short: "Import Codex auth.json through the running daemon",
		Long: `Import one or more Codex auth.json files through the daemon-side
accounts/import RPC. This exercises the same CAP/WebSocket path used by web
clients. Start capd first with "capd start".

For a direct local import that does not require the daemon, use
"capd accounts codex import".`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			provider, _ := cmd.Flags().GetString("provider")
			jsonOut, _ := cmd.Flags().GetBool("json")
			authPathsFlag, _ := cmd.Flags().GetStringArray("auth")
			params := protocol.AccountsImportParams{Provider: provider}
			authPaths := cleanImportAuthPaths(authPathsFlag)
			switch len(authPaths) {
			case 0:
			case 1:
				params.AuthPath = authPaths[0]
			default:
				params.AuthPaths = authPaths
			}
			raw, err := daemonRPCCall(cmd.Context(), "capd-accounts-import", protocol.MethodAccountsImport, params)
			if err != nil {
				return err
			}
			var result protocol.AccountsImportResult
			if err := json.Unmarshal(raw, &result); err != nil {
				return err
			}
			if jsonOut {
				out, _ := json.MarshalIndent(result, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			imported := result.Accounts
			if len(imported) == 0 && result.Account.ID != "" {
				imported = []protocol.AccountSummary{result.Account}
			}
			for _, acc := range imported {
				fmt.Fprintf(cmd.OutOrStdout(), "imported %s <%s>\n", acc.ID, acc.Email)
			}
			if result.CurrentAccountID != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "current %s\n", result.CurrentAccountID)
			}
			if step := accountsImportNextStep(result.ImportedAccounts); step != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "next: %s\n", step)
			}
			return nil
		},
	}
	importCmd.Flags().String("provider", "codex", "account provider to import")
	importCmd.Flags().StringArray("auth", nil, "path to Codex auth.json; repeat to import multiple accounts through the daemon")
	importCmd.Flags().Bool("json", false, "print accounts/import result as JSON without token material")

	checkCmd := &cobra.Command{
		Use:   "check",
		Short: "Run daemon-side account smoke checks through CAP",
		Long: `Run the daemon-side accounts/check RPC through the running capd
daemon and print safe account smoke evidence without token material or runtime
paths.

Start capd first with "capd start". For a direct local Codex account and
SecretStore smoke check that does not require the daemon, use
"capd accounts codex smoke".`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			provider, _ := cmd.Flags().GetString("provider")
			jsonOut, _ := cmd.Flags().GetBool("json")
			requireMultiple, _ := cmd.Flags().GetBool("require-multiple")
			requireFreshQuota, _ := cmd.Flags().GetBool("require-fresh-quota")
			requireAllFreshQuota, _ := cmd.Flags().GetBool("require-all-fresh-quota")
			requireSecretBackend, _ := cmd.Flags().GetString("require-secret-backend")
			refreshQuota, _ := cmd.Flags().GetBool("refresh-quota")
			readiness, _ := cmd.Flags().GetBool("readiness")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			if readiness {
				refreshQuota = true
				requireMultiple = true
				requireFreshQuota = true
				requireAllFreshQuota = true
				if strings.TrimSpace(requireSecretBackend) == "" {
					requireSecretBackend = secret.BackendNative
				}
			}
			requireSecretBackend, err := secret.NormalizeBackend(requireSecretBackend)
			if err != nil {
				return err
			}
			callCtx := cmd.Context()
			var cancel context.CancelFunc
			if timeout > 0 {
				callCtx, cancel = context.WithTimeout(callCtx, timeout)
				defer cancel()
			}
			raw, err := daemonRPCCall(callCtx, "capd-accounts-check", protocol.MethodAccountsCheck, protocol.AccountsCheckParams{
				Provider:             provider,
				RefreshQuota:         refreshQuota,
				RequireMultiple:      requireMultiple,
				RequireFreshQuota:    requireFreshQuota,
				RequireAllFreshQuota: requireAllFreshQuota,
				RequireSecretBackend: requireSecretBackend,
			})
			if err != nil {
				if jsonOut {
					cmd.SilenceUsage = true
					cmd.SilenceErrors = true
					printAccountsCheckJSONError(cmd, err)
				}
				return err
			}
			var result protocol.AccountsCheckResult
			if err := json.Unmarshal(raw, &result); err != nil {
				return err
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
			fmt.Fprintf(cmd.OutOrStdout(), "quota refreshed: %t\n", result.QuotaRefreshed)
			fmt.Fprintf(cmd.OutOrStdout(), "summary: ready=%t accounts=%d/%d missing=%d quota fresh=%d stale=%d missing=%d autoFresh=%t secretOK=%t\n",
				result.Summary.Ready,
				result.Summary.CheckedAccounts,
				result.Summary.RequiredAccounts,
				result.Summary.MissingAccounts,
				result.Summary.FreshQuotaAccounts,
				result.Summary.StaleQuotaAccounts,
				result.Summary.MissingQuotaAccounts,
				result.Summary.AutoRouteFresh,
				result.Summary.SecretBackendOK)
			if result.AutoRoute != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "auto route: %s %s\n", result.AutoRoute.AccountID, routeEvidenceText(*result.AutoRoute))
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "CURRENT\tID\tEMAIL\tSECRET\tSECRET_STATE\tCREDENTIAL\tRUNTIME\tAUTH_JSON\tMARKER\tQUOTA\tFRESH\tPRIMARY\tCHECKED_AT")
			for _, row := range result.Accounts {
				mark := ""
				if row.Current {
					mark = "*"
				}
				primary := ""
				if row.PrimaryUsedPercent != nil {
					primary = formatPercent(*row.PrimaryUsedPercent)
				}
				checkedAt := ""
				if row.QuotaCheckedAt > 0 {
					checkedAt = time.Unix(row.QuotaCheckedAt, 0).Format(time.RFC3339)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%s\t%t\t%t\t%t\t%t\t%s\t%t\t%s\t%s\n",
					mark, row.ID, row.Email, row.SecretBackendOK, row.SecretState, row.CredentialReadable, row.RuntimeReady, row.AuthJSONPrivate, row.ProjectionMarkerOK, row.QuotaState, row.QuotaFresh, primary, checkedAt)
			}
			return w.Flush()
		},
	}
	checkCmd.Flags().String("provider", "codex", "account provider to check")
	checkCmd.Flags().Bool("require-multiple", false, "fail unless at least two accounts are checked")
	checkCmd.Flags().Bool("require-fresh-quota", false, "fail unless auto-route selection is backed by fresh cached quota")
	checkCmd.Flags().Bool("require-all-fresh-quota", false, "fail unless every checked account has fresh cached quota")
	checkCmd.Flags().String("require-secret-backend", "", "fail unless daemon account check uses this SecretStore backend")
	checkCmd.Flags().Bool("refresh-quota", false, "refresh every imported Codex account quota through the daemon before checking")
	checkCmd.Flags().Bool("readiness", false, "run the daemon-side Codex readiness gate: refresh quota, require multiple accounts, fresh auto-route quota, all fresh quotas, and native SecretStore by default")
	checkCmd.Flags().Duration("timeout", 2*time.Minute, "maximum time to wait for daemon-side accounts/check")
	checkCmd.Flags().Bool("json", false, "print accounts/check result as JSON without token material")

	cmd.AddCommand(listCmd, importCmd, checkCmd, newCodexAccountsCmd())
	return cmd
}

type accountsCheckJSONError struct {
	OK        bool            `json:"ok"`
	Error     protocol.Error  `json:"error"`
	NextSteps []string        `json:"nextSteps,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

func printAccountsCheckJSONError(cmd *cobra.Command, err error) {
	perr, ok := err.(*protocol.Error)
	if !ok {
		payload := accountsCheckJSONError{
			OK: false,
			Error: protocol.Error{
				Code:    protocol.CodeInternalError,
				Message: err.Error(),
			},
		}
		out, marshalErr := json.MarshalIndent(payload, "", "  ")
		if marshalErr == nil {
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
		}
		return
	}
	payload := accountsCheckJSONError{
		OK: false,
		Error: protocol.Error{
			Code:    perr.Code,
			Message: perr.Message,
		},
	}
	if perr.Data != nil {
		if data, marshalErr := json.Marshal(perr.Data); marshalErr == nil && string(data) != "null" {
			payload.Data = data
			var partial protocol.AccountsCheckResult
			if err := json.Unmarshal(data, &partial); err == nil {
				payload.NextSteps = accountsCheckErrorNextSteps(perr.Message, partial)
			}
		}
	}
	out, marshalErr := json.MarshalIndent(payload, "", "  ")
	if marshalErr != nil {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(out))
}

func accountsCheckErrorNextSteps(message string, partial protocol.AccountsCheckResult) []string {
	steps := []string{}
	summary := partial.Summary
	if accountsCheckHasSecretState(partial, protocol.AccountSecretStateAccessDenied) || strings.Contains(message, "macOS keychain status -128") {
		steps = append(steps, "approve macOS Keychain access, or avoid native prompts by restarting with: capd start --secret-backend file and re-importing accounts with: capd accounts --secret-backend file codex import --auth /path/to/auth.json")
	}
	if accountsCheckHasSecretState(partial, protocol.AccountSecretStateTimeout) {
		steps = append(steps, "unlock or approve OS SecretStore access, then rerun: capd accounts check --json --readiness --timeout 2m")
	}
	requiredBackend := summary.RequiredSecretBackend
	if requiredBackend == "" && strings.Contains(message, `want "native"`) {
		requiredBackend = secret.BackendNative
	}
	if requiredBackend == "" && strings.Contains(message, `want "file"`) {
		requiredBackend = secret.BackendFile
	}
	if requiredBackend != "" && !summary.SecretBackendOK {
		steps = append(steps, "restart capd with: capd start --secret-backend "+requiredBackend)
	}
	if summary.MissingAccounts > 0 || strings.Contains(message, "expected multiple Codex accounts") {
		backend := partial.SecretBackend
		if requiredBackend != "" {
			backend = requiredBackend
		}
		steps = append(steps, "import another Codex account through CAP with: capd accounts import --auth /path/to/auth.json")
		if backend != "" {
			steps = append(steps, "or import locally with: "+codexLocalImportNextStep(backend, true))
		}
	}
	if strings.Contains(message, "fresh cached quota") || strings.Contains(message, "quota is not fresh") || (!summary.AutoRouteFresh && summary.CheckedAccounts > 0) || summary.StaleQuotaAccounts > 0 || summary.MissingQuotaAccounts > 0 {
		steps = append(steps, "refresh and verify daemon-side readiness with: capd accounts check --json --readiness")
	}
	return compactStrings(steps)
}

func accountsCheckHasSecretState(result protocol.AccountsCheckResult, state string) bool {
	for _, row := range result.Accounts {
		if row.SecretState == state {
			return true
		}
	}
	return false
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
			authPathsFlag, _ := cmd.Flags().GetStringArray("auth")
			authPaths, err := codexImportAuthPaths(authPathsFlag)
			if err != nil {
				return err
			}
			accounts, secrets, err := accountDepsFromCmd(cmd)
			if err != nil {
				return err
			}
			defer accounts.Close()
			importer := codexauth.Importer{Accounts: accounts, Secrets: secrets}
			for _, path := range authPaths {
				result, err := importer.ImportAuthJSON(cmd.Context(), path)
				if err != nil {
					return fmt.Errorf("import account: %s", codexauth.SafeImportError(err, path))
				}
				fmt.Fprintf(cmd.OutOrStdout(), "imported %s", result.Account.ID)
				if result.Account.Email != "" {
					fmt.Fprintf(cmd.OutOrStdout(), " <%s>", result.Account.Email)
				}
				fmt.Fprintln(cmd.OutOrStdout())
			}
			return nil
		},
	}
	importCmd.Flags().StringArray("auth", nil, "path to Codex auth.json; repeat to import multiple accounts (default: ~/.codex/auth.json, or CAPD_CODEX_AUTH_PATHS when set)")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List Codex accounts imported into capd",
		RunE: func(cmd *cobra.Command, _ []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
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
			if jsonOut {
				rows := makeAccountListRows(accounts, list, current)
				out, _ := json.MarshalIndent(rows, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			rows := makeAccountListRows(accounts, list, current)
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "CURRENT\tID\tMODE\tEMAIL\tCHATGPT_ACCOUNT\tPLAN\tSECRET_BACKEND\tPRIMARY_USED\tQUOTA_STATE\tFRESH\tROUTE_SCORE")
			for _, row := range rows {
				mark := ""
				if row.Current {
					mark = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%t\t%s\n", mark, row.ID, row.AuthMode, row.Email, row.AccountID, row.Plan, row.SecretBackend, row.PrimaryUsed, row.QuotaState, row.QuotaFresh, routeScoreText(row.RouteScore))
			}
			return w.Flush()
		},
	}
	listCmd.Flags().Bool("json", false, "print imported Codex account metadata as JSON without token material")

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
				if err := rejectConcreteCodexAccountArg(args[0]); err != nil {
					return err
				}
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
				if err := rejectConcreteCodexAccountArg(id); err != nil {
					return err
				}
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
			if err := rejectConcreteCodexAccountArg(args[0]); err != nil {
				return err
			}
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

	migrateSecretsCmd := &cobra.Command{
		Use:   "migrate-secrets [account-id|all]",
		Short: "Move imported Codex account credentials between SecretStore backends",
		Long: `Move imported Codex account credentials between SecretStore backends and
update account metadata to point at the new safe secret reference.

By default this migrates every imported Codex account from the file backend to
the native OS backend and keeps the source secret as a rollback path. Add
--delete-source only after a successful readiness check.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fromBackend, _ := cmd.Flags().GetString("from")
			toBackend, _ := cmd.Flags().GetString("to")
			deleteSource, _ := cmd.Flags().GetBool("delete-source")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			jsonOut, _ := cmd.Flags().GetBool("json")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			fromBackend, err := secret.NormalizeBackend(fromBackend)
			if err != nil {
				return err
			}
			toBackend, err = secret.NormalizeBackend(toBackend)
			if err != nil {
				return err
			}
			if fromBackend == "" {
				fromBackend = secret.BackendFile
			}
			if toBackend == "" {
				toBackend = secret.BackendNative
			}
			if fromBackend == toBackend {
				return fmt.Errorf("source and target secret backends are both %q", fromBackend)
			}
			targetID := protocol.AccountAll
			if len(args) == 1 {
				targetID = strings.TrimSpace(args[0])
				if targetID == "" {
					targetID = protocol.AccountAll
				}
				if targetID != protocol.AccountAll {
					if err := rejectConcreteCodexAccountArg(targetID); err != nil {
						return err
					}
				}
			}
			migrateCtx := cmd.Context()
			var cancel context.CancelFunc
			if timeout > 0 {
				migrateCtx, cancel = context.WithTimeout(migrateCtx, timeout)
				defer cancel()
			}
			accounts, sourceStore, targetStore, err := codexSecretMigrationDeps(fromBackend, toBackend)
			if err != nil {
				return err
			}
			defer accounts.Close()
			result, err := migrateCodexAccountSecrets(migrateCtx, accounts, sourceStore, targetStore, codexSecretMigrationOptions{
				AccountID:     targetID,
				DeleteSource:  deleteSource,
				DryRun:        dryRun,
				SourceBackend: fromBackend,
				TargetBackend: toBackend,
			})
			if jsonOut {
				out, _ := json.MarshalIndent(result, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
			} else {
				printCodexSecretMigrationResult(cmd, result)
			}
			if err != nil {
				cmd.SilenceUsage = true
				return err
			}
			return nil
		},
	}
	migrateSecretsCmd.Flags().String("from", secret.BackendFile, "source SecretStore backend")
	migrateSecretsCmd.Flags().String("to", secret.BackendNative, "target SecretStore backend")
	migrateSecretsCmd.Flags().Bool("delete-source", false, "delete each source secret after its account metadata is updated")
	migrateSecretsCmd.Flags().Bool("dry-run", false, "check which accounts would migrate without writing target secrets")
	migrateSecretsCmd.Flags().Duration("timeout", 2*time.Minute, "maximum time to wait for SecretStore reads and writes")
	migrateSecretsCmd.Flags().Bool("json", false, "print migration evidence without token material")

	quotaCmd := &cobra.Command{
		Use:   "quota [account-id|auto|all]",
		Short: "Fetch ChatGPT backend quota for imported Codex accounts",
		Long:  "Fetch ChatGPT backend quota for imported Codex accounts. With auto, capd uses the same conservative quota scoring rule as account-aware routing. With all, capd refreshes every imported Codex account and prints safe summaries.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseURL, _ := cmd.Flags().GetString("base-url")
			rawOut, _ := cmd.Flags().GetBool("raw")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			callCtx := cmd.Context()
			var cancel context.CancelFunc
			if timeout > 0 {
				callCtx, cancel = context.WithTimeout(callCtx, timeout)
				defer cancel()
			}
			accounts, secrets, err := accountDepsFromCmd(cmd)
			if err != nil {
				return err
			}
			defer accounts.Close()
			id := ""
			if len(args) == 1 {
				id = strings.TrimSpace(args[0])
			} else {
				id, err = accounts.CurrentAccount(codexauth.Provider)
				if err != nil {
					return err
				}
				id = strings.TrimSpace(id)
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
				sort.Slice(list, func(i, j int) bool {
					return list[i].ID < list[j].ID
				})
				rows := make([]codexQuotaSummary, 0, len(list))
				for _, acc := range list {
					row, err := refreshCodexQuota(callCtx, accounts, secrets, baseURL, acc)
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
			summary, usage, err := refreshCodexQuotaWithUsage(callCtx, accounts, secrets, baseURL, acc)
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
	quotaCmd.Flags().Duration("timeout", 2*time.Minute, "maximum time to wait for local quota refresh and SecretStore reads")

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
			timeout, _ := cmd.Flags().GetDuration("timeout")
			requireSecretBackend, err := secret.NormalizeBackend(requireSecretBackend)
			if err != nil {
				return err
			}
			checkCtx := cmd.Context()
			var cancel context.CancelFunc
			if timeout > 0 {
				checkCtx, cancel = context.WithTimeout(checkCtx, timeout)
				defer cancel()
			}
			accounts, secrets, err := accountDepsFromCmd(cmd)
			if err != nil {
				return err
			}
			defer accounts.Close()
			list, err := accounts.ListAccounts(codexauth.Provider)
			if err != nil {
				return err
			}
			sort.Slice(list, func(i, j int) bool {
				return list[i].ID < list[j].ID
			})
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
			if len(list) == 0 {
				return codexSmokeFail(cmd, jsonOut, result, "no imported Codex accounts; run capd accounts codex import first", codexLocalImportNextStep(result.SecretBackend, false))
			}
			populateCodexSmokeCachedEvidence(&result, accounts, list)
			if requireMultiple && len(list) < 2 {
				return codexSmokeFail(cmd, jsonOut, result, fmt.Sprintf("expected multiple Codex accounts, found %d", len(list)), codexLocalImportNextStep(result.SecretBackend, true))
			}
			if requireSecretBackend != "" && requireSecretBackend != result.SecretBackend {
				return codexSmokeFail(cmd, jsonOut, result, fmt.Sprintf("secret backend = %q, want %q", result.SecretBackend, requireSecretBackend), "rerun with CAPD_SECRET_BACKEND="+requireSecretBackend)
			}
			result.Accounts = result.Accounts[:0]
			for _, acc := range list {
				ref, err := secret.ParseRef(acc.SecretRef)
				if err != nil {
					row := codexSmokeSecretFailureRow(acc, false, protocol.AccountSecretStateMalformedRef)
					result.Accounts = append(result.Accounts, row)
					return codexSmokeFail(cmd, jsonOut, result, fmt.Sprintf("%s: parse secret ref: malformed", acc.ID), "remove and re-import malformed Codex account metadata")
				}
				if err := secret.EnsureRefBackend(secrets, ref); err != nil {
					row := codexSmokeSecretFailureRow(acc, false, protocol.AccountSecretStateBackendMismatch)
					result.Accounts = append(result.Accounts, row)
					return codexSmokeFail(cmd, jsonOut, result, fmt.Sprintf("%s: secret backend mismatch", acc.ID), "rerun with CAPD_SECRET_BACKEND="+secretRefBackendLabel(ref)+" or re-import the account with the active SecretStore backend")
				}
				bundle, err := secrets.Get(checkCtx, ref)
				if err != nil {
					state := codexSmokeSecretErrorState(err)
					row := codexSmokeSecretFailureRow(acc, true, state)
					result.Accounts = append(result.Accounts, row)
					return codexSmokeFail(cmd, jsonOut, result, fmt.Sprintf("%s: load secret: %s", acc.ID, state), codexSmokeSecretNextStep(state, result.SecretBackend))
				}
				profile, err := codexauth.RuntimeProjector{
					Root:    filepath.Join(home, "runtimes"),
					Secrets: secrets,
				}.Project(checkCtx, acc)
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
					SecretState:        protocol.AccountSecretStateReadable,
				}
				if refreshQuota {
					if bundle.AccountID == "" {
						bundle.AccountID = acc.AccountID
					}
					updatedAcc, changed := codexauth.AccountWithBundleMetadata(acc, bundle)
					quotaResult, err := codexquota.Client{BaseURL: baseURL}.Usage(checkCtx, acc.ID, bundle)
					if err != nil {
						return fmt.Errorf("%s: refresh quota: %w", acc.ID, err)
					}
					if updatedAcc.Plan == "" && quotaResult.Quota.Plan != "" {
						updatedAcc.Plan = quotaResult.Quota.Plan
						changed = true
					}
					if changed {
						if err := accounts.UpsertAccount(updatedAcc); err != nil {
							return fmt.Errorf("%s: update account metadata: %w", acc.ID, err)
						}
						acc = updatedAcc
						row.Email = acc.Email
						row.AuthMode = acc.AuthMode
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
				result.Accounts = append(result.Accounts, row)
			}
			if route, err := codexSmokeAutoRouteEvidence(accounts); err != nil {
				return err
			} else {
				result.AutoRoute = route
			}
			if candidates, err := account.QuotaRouteCandidates(accounts, codexauth.Provider); err == nil {
				result.RouteCandidates = candidates
			}
			if requireAllFreshQuota {
				var stale []string
				for _, row := range result.Accounts {
					if !row.QuotaFresh {
						stale = append(stale, fmt.Sprintf("%s=%s", row.ID, row.QuotaState))
					}
				}
				if len(stale) > 0 {
					return codexSmokeFail(cmd, jsonOut, result, "quota is not fresh for "+strings.Join(stale, ", "), "run with --quota or refresh every account first")
				}
			}
			if requireFreshQuota && (result.AutoRoute == nil || !result.AutoRoute.Fresh) {
				return codexSmokeFail(cmd, jsonOut, result, "auto route does not have fresh cached quota", "run with --quota or refresh quota first")
			}
			if jsonOut {
				out, _ := json.MarshalIndent(result, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tEMAIL\tPROJECTED_CODEX_HOME\tAUTH_MODE\tSECRET_STATE\tCREDENTIAL\tRUNTIME\tAUTH_JSON\tMARKER\tQUOTA\tFRESH\tPRIMARY\tCHECKED_AT")
			for _, row := range result.Accounts {
				checkedAt := ""
				if row.QuotaCheckedAt > 0 {
					checkedAt = time.Unix(row.QuotaCheckedAt, 0).Format(time.RFC3339)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%t\t%t\t%t\t%t\t%s\t%t\t%s\t%s\n",
					row.ID, row.Email, row.ProjectedCodexHome, row.AuthMode, emptyDash(row.SecretState),
					row.SecretReadable, row.RuntimeEnvOK, row.AuthJSONPrivate, row.ProjectionMarkerOK,
					row.QuotaState, row.QuotaFresh, row.PrimaryUsed, checkedAt)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if result.AutoRoute != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "auto route: %s %s (%s)\n", result.AutoRoute.AccountID, smokeRouteEvidenceText(*result.AutoRoute), result.AutoRoute.Reason)
			}
			if len(result.RouteCandidates) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "route candidates:")
				for _, candidate := range result.RouteCandidates {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s %s\n", candidate.AccountID, routeEvidenceText(candidate))
				}
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
	smokeCmd.Flags().Duration("timeout", 2*time.Minute, "maximum time to wait for local smoke checks, SecretStore reads, projections, and quota refresh")
	smokeCmd.Flags().Bool("json", false, "print machine-readable smoke evidence without token material")

	cmd.AddCommand(importCmd, listCmd, currentCmd, projectCmd, removeCmd, migrateSecretsCmd, quotaCmd, smokeCmd)
	return cmd
}

func accountsImportNextStep(importedAccounts int) string {
	if importedAccounts <= 0 {
		return ""
	}
	if importedAccounts < 2 {
		return "import a second Codex account with: capd accounts import --auth /path/to/auth.json"
	}
	return "verify readiness with: capd accounts check --readiness"
}

func codexLocalImportNextStep(secretBackend string, second bool) string {
	cmd := "capd accounts codex import"
	if secretBackend != "" && secretBackend != secret.BackendFile {
		cmd = "capd accounts --secret-backend " + secretBackend + " codex import"
	}
	if second {
		return "import a second Codex account with: " + cmd + " --auth /path/to/auth.json"
	}
	return "import Codex auth with: " + cmd
}

func smokeRouteEvidenceText(route codexSmokeAutoRoute) string {
	parts := []string{"quota " + route.QuotaState, fmt.Sprintf("fresh %t", route.Fresh)}
	if route.Primary != nil {
		parts = append(parts, "primary "+formatPercent(*route.Primary))
	}
	parts = append(parts, fmt.Sprintf("score %.2f", route.Score))
	if route.CheckedAt > 0 {
		parts = append(parts, "checked "+time.Unix(route.CheckedAt, 0).Format(time.RFC3339))
	}
	return strings.Join(parts, " ")
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
	OK              bool                            `json:"ok"`
	CheckedAccounts int                             `json:"checkedAccounts"`
	QuotaRefreshed  bool                            `json:"quotaRefreshed"`
	SecretBackend   string                          `json:"secretBackend"`
	AutoRoute       *codexSmokeAutoRoute            `json:"autoRoute,omitempty"`
	RouteCandidates []protocol.AccountRouteEvidence `json:"routeCandidates,omitempty"`
	Accounts        []codexSmokeAccount             `json:"accounts"`
	Issues          []string                        `json:"issues,omitempty"`
	NextSteps       []string                        `json:"nextSteps,omitempty"`
}

func codexSmokeFail(cmd *cobra.Command, jsonOut bool, result codexSmokeResult, issue, nextStep string) error {
	result.OK = false
	if issue != "" {
		result.Issues = append(result.Issues, issue)
	}
	if nextStep != "" {
		result.NextSteps = append(result.NextSteps, nextStep)
	}
	if jsonOut {
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
	}
	if issue == "" {
		issue = "Codex account smoke check failed"
	}
	return fmt.Errorf("%s", issue)
}

func codexSmokeSecretFailureRow(acc account.Account, backendOK bool, state string) codexSmokeAccount {
	return codexSmokeAccount{
		ID:              acc.ID,
		Email:           acc.Email,
		AuthMode:        acc.AuthMode,
		SecretBackendOK: backendOK,
		SecretReadable:  false,
		SecretState:     state,
		PrimaryUsed:     "cached-missing",
		QuotaState:      protocol.AccountQuotaStateMissing,
	}
}

func codexSmokeSecretErrorState(err error) string {
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

func codexSmokeSecretNextStep(state, backend string) string {
	switch state {
	case protocol.AccountSecretStateAccessDenied:
		return "approve macOS Keychain access, or avoid native prompts by restarting with: capd start --secret-backend file and re-importing accounts with: capd accounts --secret-backend file codex import --auth /path/to/auth.json"
	case protocol.AccountSecretStateTimeout:
		return "unlock or approve OS SecretStore access, then rerun: capd accounts codex smoke --json --timeout 2m"
	case protocol.AccountSecretStateMissing:
		return codexLocalImportNextStep(backend, false)
	default:
		return "re-import the failing Codex account with: capd accounts codex import --auth /path/to/auth.json"
	}
}

func populateCodexSmokeCachedEvidence(result *codexSmokeResult, accounts *account.Store, list []account.Account) {
	if result == nil || accounts == nil || len(list) == 0 {
		return
	}
	if len(result.Accounts) == 0 {
		result.Accounts = make([]codexSmokeAccount, 0, len(list))
		for _, acc := range list {
			result.Accounts = append(result.Accounts, codexSmokeCachedAccountRow(accounts, acc))
		}
	}
	if result.AutoRoute == nil {
		if route, err := codexSmokeAutoRouteEvidence(accounts); err == nil {
			result.AutoRoute = route
		}
	}
	if len(result.RouteCandidates) == 0 {
		if candidates, err := account.QuotaRouteCandidates(accounts, codexauth.Provider); err == nil {
			result.RouteCandidates = candidates
		}
	}
}

func codexSmokeCachedAccountRow(accounts *account.Store, acc account.Account) codexSmokeAccount {
	row := codexSmokeAccount{
		ID:          acc.ID,
		Email:       acc.Email,
		AuthMode:    acc.AuthMode,
		PrimaryUsed: "cached-missing",
		QuotaState:  protocol.AccountQuotaStateMissing,
	}
	if q, err := accounts.LoadQuota(acc.ID); err == nil {
		setCodexSmokeQuotaEvidence(&row, q)
	}
	return row
}

func secretRefBackendLabel(ref secret.Ref) string {
	if ref.Backend == "" {
		return secret.BackendFile
	}
	return ref.Backend
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
	SecretState        string   `json:"secretState,omitempty"`
	PrimaryUsed        string   `json:"primaryUsed"`
	PrimaryUsedPercent *float64 `json:"primaryUsedPercent,omitempty"`
	QuotaState         string   `json:"quotaState"`
	QuotaFresh         bool     `json:"quotaFresh"`
	QuotaCheckedAt     int64    `json:"quotaCheckedAt,omitempty"`
}

type codexSecretMigrationOptions struct {
	AccountID     string
	DeleteSource  bool
	DryRun        bool
	SourceBackend string
	TargetBackend string
}

type codexSecretMigrationResult struct {
	OK            bool                      `json:"ok"`
	Provider      string                    `json:"provider"`
	SourceBackend string                    `json:"sourceBackend"`
	TargetBackend string                    `json:"targetBackend"`
	DeleteSource  bool                      `json:"deleteSource,omitempty"`
	DryRun        bool                      `json:"dryRun,omitempty"`
	Migrated      []codexSecretMigrationRow `json:"migrated,omitempty"`
	Skipped       []codexSecretMigrationRow `json:"skipped,omitempty"`
	Issues        []string                  `json:"issues,omitempty"`
	NextSteps     []string                  `json:"nextSteps,omitempty"`
}

type codexSecretMigrationRow struct {
	ID       string `json:"id"`
	Email    string `json:"email,omitempty"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	Provider string `json:"provider,omitempty"`
}

func codexSecretMigrationDeps(fromBackend, toBackend string) (*account.Store, secret.Store, secret.Store, error) {
	home, err := daemon.Home()
	if err != nil {
		return nil, nil, nil, err
	}
	accounts, err := account.OpenStore(filepath.Join(home, "accounts.db"))
	if err != nil {
		return nil, nil, nil, err
	}
	sourceStore, err := secret.Open(filepath.Join(home, "secrets", "codex"), fromBackend)
	if err != nil {
		accounts.Close()
		return nil, nil, nil, err
	}
	targetStore, err := secret.Open(filepath.Join(home, "secrets", "codex"), toBackend)
	if err != nil {
		accounts.Close()
		return nil, nil, nil, err
	}
	return accounts, sourceStore, targetStore, nil
}

func migrateCodexAccountSecrets(ctx context.Context, accounts *account.Store, sourceStore, targetStore secret.Store, opts codexSecretMigrationOptions) (codexSecretMigrationResult, error) {
	result := codexSecretMigrationResult{
		OK:            true,
		Provider:      codexauth.Provider,
		SourceBackend: sourceStore.Backend(),
		TargetBackend: targetStore.Backend(),
		DeleteSource:  opts.DeleteSource,
		DryRun:        opts.DryRun,
	}
	if opts.SourceBackend != "" {
		result.SourceBackend = opts.SourceBackend
	}
	if opts.TargetBackend != "" {
		result.TargetBackend = opts.TargetBackend
	}
	targetID := strings.TrimSpace(opts.AccountID)
	if targetID == "" {
		targetID = protocol.AccountAll
	}
	list, err := codexSecretMigrationAccounts(accounts, targetID)
	if err != nil {
		result.OK = false
		result.Issues = append(result.Issues, err.Error())
		return result, err
	}
	if len(list) == 0 {
		err := fmt.Errorf("no imported Codex accounts")
		result.OK = false
		result.Issues = append(result.Issues, err.Error())
		result.NextSteps = append(result.NextSteps, "import Codex auth with: capd accounts codex import")
		return result, err
	}
	for _, acc := range list {
		row, err := migrateCodexAccountSecret(ctx, accounts, sourceStore, targetStore, acc, opts)
		if row.Status == "skipped" {
			result.Skipped = append(result.Skipped, row)
			continue
		}
		if row.ID != "" {
			result.Migrated = append(result.Migrated, row)
		}
		if err != nil {
			result.OK = false
			result.Issues = append(result.Issues, acc.ID+": "+err.Error())
			result.NextSteps = append(result.NextSteps, "fix the failing SecretStore entry, then rerun migrate-secrets")
			return result, err
		}
	}
	if len(result.Migrated) == 0 && len(result.Skipped) > 0 {
		result.NextSteps = append(result.NextSteps, "rerun with --from matching the account secret backend or choose a specific account")
	}
	if len(result.Migrated) > 0 {
		result.NextSteps = append(result.NextSteps, "restart capd with CAPD_SECRET_BACKEND="+result.TargetBackend+" and run: CAPD_SECRET_BACKEND="+result.TargetBackend+" capd accounts check --json --readiness --require-secret-backend "+result.TargetBackend)
		if !opts.DeleteSource {
			result.NextSteps = append(result.NextSteps, "after readiness passes, rerun with --delete-source to remove old source secrets")
		}
	}
	return result, nil
}

func codexSecretMigrationAccounts(accounts *account.Store, targetID string) ([]account.Account, error) {
	if targetID == protocol.AccountAll {
		list, err := accounts.ListAccounts(codexauth.Provider)
		if err != nil {
			return nil, err
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].ID < list[j].ID
		})
		return list, nil
	}
	acc, err := accounts.LoadAccount(targetID)
	if err != nil {
		return nil, err
	}
	if acc.Provider != codexauth.Provider {
		return nil, fmt.Errorf("account %q belongs to provider %q, not %q", acc.ID, acc.Provider, codexauth.Provider)
	}
	return []account.Account{acc}, nil
}

func migrateCodexAccountSecret(ctx context.Context, accounts *account.Store, sourceStore, targetStore secret.Store, acc account.Account, opts codexSecretMigrationOptions) (codexSecretMigrationRow, error) {
	row := codexSecretMigrationRow{
		ID:       acc.ID,
		Email:    acc.Email,
		Provider: acc.Provider,
		Status:   "migrated",
	}
	ref, err := secret.ParseRef(acc.SecretRef)
	if err != nil {
		row.Status = "failed"
		row.Reason = "malformed-ref"
		return row, errors.New(row.Reason)
	}
	row.From = ref.Backend
	if row.From == "" {
		row.From = sourceStore.Backend()
	}
	if row.From != sourceStore.Backend() {
		row.Status = "skipped"
		row.Reason = "source-backend-mismatch"
		return row, nil
	}
	if opts.DryRun {
		row.Status = "dry-run"
		row.To = secret.Ref{Backend: targetStore.Backend(), ID: acc.ID}.String()
		return row, nil
	}
	bundle, err := sourceStore.Get(ctx, ref)
	if err != nil {
		row.Status = "failed"
		row.Reason = "source-unreadable"
		return row, errors.New(row.Reason)
	}
	newRef, err := targetStore.Put(ctx, acc.ID, bundle)
	if err != nil {
		row.Status = "failed"
		row.Reason = "target-unwritable"
		return row, errors.New(row.Reason)
	}
	if _, err := targetStore.Get(ctx, newRef); err != nil {
		_ = targetStore.Delete(ctx, newRef)
		row.Status = "failed"
		row.Reason = "target-unreadable"
		return row, errors.New(row.Reason)
	}
	row.To = newRef.String()
	updated, _ := codexauth.AccountWithBundleMetadata(acc, bundle)
	updated.SecretRef = newRef.String()
	if err := accounts.UpsertAccount(updated); err != nil {
		_ = targetStore.Delete(ctx, newRef)
		row.Status = "failed"
		row.Reason = "metadata-update-failed"
		return row, errors.New(row.Reason)
	}
	if opts.DeleteSource {
		if err := sourceStore.Delete(ctx, ref); err != nil {
			row.Status = "failed"
			row.Reason = "source-delete-failed"
			return row, errors.New(row.Reason)
		}
	}
	return row, nil
}

func printCodexSecretMigrationResult(cmd *cobra.Command, result codexSecretMigrationResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "provider: %s\n", result.Provider)
	fmt.Fprintf(cmd.OutOrStdout(), "secret backend: %s -> %s\n", result.SourceBackend, result.TargetBackend)
	fmt.Fprintf(cmd.OutOrStdout(), "dry run: %t\n", result.DryRun)
	fmt.Fprintf(cmd.OutOrStdout(), "delete source: %t\n", result.DeleteSource)
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "STATUS\tID\tEMAIL\tFROM\tTO\tREASON")
	for _, row := range result.Migrated {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", row.Status, row.ID, row.Email, row.From, row.To, row.Reason)
	}
	for _, row := range result.Skipped {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", row.Status, row.ID, row.Email, row.From, row.To, row.Reason)
	}
	_ = w.Flush()
	for _, issue := range result.Issues {
		fmt.Fprintf(cmd.OutOrStdout(), "issue: %s\n", issue)
	}
	for _, next := range result.NextSteps {
		fmt.Fprintf(cmd.OutOrStdout(), "next: %s\n", next)
	}
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
	updatedAcc, changed := codexauth.AccountWithBundleMetadata(acc, bundle)
	result, err := codexquota.Client{BaseURL: baseURL}.Usage(ctx, acc.ID, bundle)
	if err != nil {
		return codexQuotaSummary{}, nil, err
	}
	if updatedAcc.Plan == "" && result.Quota.Plan != "" {
		updatedAcc.Plan = result.Quota.Plan
		changed = true
	}
	if changed {
		if err := accounts.UpsertAccount(updatedAcc); err != nil {
			return codexQuotaSummary{}, nil, err
		}
		acc = updatedAcc
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
	Current        bool     `json:"current"`
	Provider       string   `json:"provider"`
	ID             string   `json:"id"`
	AuthMode       string   `json:"authMode,omitempty"`
	Email          string   `json:"email,omitempty"`
	AccountID      string   `json:"accountId,omitempty"`
	Plan           string   `json:"plan,omitempty"`
	SecretBackend  string   `json:"secretBackend,omitempty"`
	PrimaryUsed    string   `json:"primaryUsed,omitempty"`
	QuotaState     string   `json:"quotaState"`
	QuotaFresh     bool     `json:"quotaFresh"`
	QuotaCheckedAt int64    `json:"quotaCheckedAt,omitempty"`
	RouteScore     *float64 `json:"routeScore,omitempty"`
	RouteReason    string   `json:"routeReason,omitempty"`
}

func makeAccountListRows(accounts *account.Store, list []account.Account, current string) []accountListRow {
	rows := make([]accountListRow, 0, len(list))
	for _, acc := range list {
		rows = append(rows, makeAccountListRow(accounts, acc, current))
	}
	return rows
}

func makeAccountListRow(accounts *account.Store, acc account.Account, current string) accountListRow {
	row := accountListRow{
		Current:       acc.ID == current,
		ID:            acc.ID,
		Provider:      acc.Provider,
		AuthMode:      acc.AuthMode,
		Email:         acc.Email,
		AccountID:     acc.AccountID,
		Plan:          acc.Plan,
		SecretBackend: accountSecretBackend(acc.SecretRef),
		QuotaState:    protocol.AccountQuotaStateMissing,
	}
	if q, err := accounts.LoadQuota(acc.ID); err == nil {
		if row.Plan == "" {
			row.Plan = q.Plan
		}
		row.PrimaryUsed = formatPercent(q.PrimaryUsedPercent)
		row.QuotaState = accountQuotaState(q)
		row.QuotaFresh = row.QuotaState == protocol.AccountQuotaStateFresh
		row.QuotaCheckedAt = q.CheckedAt
	}
	if acc.Provider == codexauth.Provider {
		score := account.QuotaRouteScore(accounts, acc, current)
		row.RouteScore = &score
		row.RouteReason = account.QuotaRouteReason(accounts, acc)
	}
	return row
}

func routeScoreText(score *float64) string {
	if score == nil {
		return ""
	}
	return fmt.Sprintf("%.2f", *score)
}

func accountSecretBackend(secretRef string) string {
	ref, err := secret.ParseRef(secretRef)
	if err != nil {
		return "malformed"
	}
	if ref.Backend == "" {
		return secret.BackendFile
	}
	return ref.Backend
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
	evidence := account.QuotaRouteEvidence(accounts, acc)
	route := &codexSmokeAutoRoute{
		AccountID:  evidence.AccountID,
		Reason:     evidence.Reason,
		Score:      evidence.Score,
		QuotaState: evidence.QuotaState,
		Fresh:      evidence.Fresh,
		CheckedAt:  evidence.CheckedAt,
		Primary:    evidence.PrimaryUsedPercent,
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

func rejectConcreteCodexAccountArg(id string) error {
	id = strings.TrimSpace(id)
	switch id {
	case protocol.AccountAll:
		return fmt.Errorf("account id %q is reserved for quota batch refresh", protocol.AccountAll)
	case protocol.AccountAuto:
		return fmt.Errorf("account id %q is supported only for account-aware routing", protocol.AccountAuto)
	default:
		return nil
	}
}

func codexImportAuthPaths(authPaths []string) ([]string, error) {
	var explicit []string
	for _, raw := range authPaths {
		if path := strings.TrimSpace(raw); path != "" {
			explicit = append(explicit, path)
		}
	}
	if len(explicit) > 0 {
		return explicit, nil
	}
	if raw := strings.TrimSpace(os.Getenv("CAPD_CODEX_AUTH_PATHS")); raw != "" {
		var paths []string
		for _, path := range filepath.SplitList(raw) {
			if path = strings.TrimSpace(path); path != "" {
				paths = append(paths, path)
			}
		}
		if len(paths) > 0 {
			return paths, nil
		}
	}
	path, err := codexauth.DefaultAuthPath("")
	if err != nil {
		return nil, err
	}
	return []string{path}, nil
}

func cleanImportAuthPaths(authPaths []string) []string {
	var clean []string
	for _, raw := range authPaths {
		if path := strings.TrimSpace(raw); path != "" {
			clean = append(clean, path)
		}
	}
	return clean
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
