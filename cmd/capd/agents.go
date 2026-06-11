package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/daemon"
	"github.com/codingagentprotocol/capd/internal/discovery"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func newAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Inspect coding agent CLIs on this machine",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "Probe and list discovered agent CLIs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			infos := discovery.Discover(cmd.Context(), daemon.Registry())
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTATUS\tVERSION\tBIN")
			for _, a := range infos {
				status := "not installed"
				if a.Available {
					status = "available"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.ID, status, a.Version, a.Bin)
			}
			return w.Flush()
		},
	})
	usageCmd := &cobra.Command{
		Use:   "usage <agent-id>",
		Short: "Account usage and rate limits for one agent (plan, used %, reset times)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			accountID, _ := cmd.Flags().GetString("account")
			a, ok := daemon.Registry().Get(args[0])
			if !ok {
				return fmt.Errorf("unknown agent %q", args[0])
			}
			up, ok := a.(adapter.UsageProvider)
			if !ok {
				return fmt.Errorf("agent %q does not report usage", args[0])
			}
			var usage map[string]any
			var err error
			if accountID != "" {
				accountUp, ok := a.(adapter.AccountUsageProvider)
				if !ok {
					return fmt.Errorf("agent %q does not report account-specific usage", args[0])
				}
				accounts, secrets, err := openAccountDeps()
				if err != nil {
					return err
				}
				defer accounts.Close()
				acc, err := resolveUsageAccount(accounts, accountID)
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
				usage, err = accountUp.UsageFor(cmd.Context(), adapter.SessionOpts{Env: profile.Env})
				if err == nil {
					if err := accounts.SaveQuota(account.QuotaFromUsage(acc.ID, usage)); err != nil {
						return err
					}
				}
			} else {
				usage, err = up.Usage(cmd.Context())
			}
			if err != nil {
				return err
			}
			out, _ := json.MarshalIndent(usage, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}
	usageCmd.Flags().String("account", "", "imported account id for account-specific usage (currently Codex)")
	cmd.AddCommand(usageCmd)
	return cmd
}

func resolveUsageAccount(accounts *account.Store, accountID string) (account.Account, error) {
	if accountID == protocol.AccountAuto {
		acc, err := account.SelectLowestQuotaAccount(accounts, codexauth.Provider)
		if errors.Is(err, account.ErrUnknownAccount) {
			return account.Account{}, fmt.Errorf("no imported Codex accounts; run capd accounts codex import first")
		}
		return acc, err
	}
	acc, err := accounts.LoadAccount(accountID)
	if err != nil {
		return account.Account{}, err
	}
	if acc.Provider != codexauth.Provider {
		return account.Account{}, fmt.Errorf("account %q is not a Codex account", accountID)
	}
	return acc, nil
}
