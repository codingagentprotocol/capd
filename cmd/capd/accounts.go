package main

import (
	"fmt"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/daemon"
)

func newAccountsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "accounts",
		Short: "Manage local agent accounts",
	}
	cmd.AddCommand(newCodexAccountsCmd())
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
			fmt.Fprintln(w, "CURRENT\tID\tMODE\tEMAIL\tCHATGPT_ACCOUNT")
			for _, acc := range list {
				mark := ""
				if acc.ID == current {
					mark = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", mark, acc.ID, acc.AuthMode, acc.Email, acc.AccountID)
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

	cmd.AddCommand(importCmd, listCmd, currentCmd, projectCmd)
	return cmd
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
	secrets := secret.NewFileStore(filepath.Join(home, "secrets", "codex"))
	return accounts, secrets, nil
}
