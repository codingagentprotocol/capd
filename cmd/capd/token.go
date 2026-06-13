package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/config"
	"github.com/codingagentprotocol/capd/internal/daemon"
	"github.com/codingagentprotocol/capd/internal/security"
)

func newTokenCmd() *cobra.Command {
	var url bool
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Print the local daemon token for browser clients",
		RunE: func(cmd *cobra.Command, _ []string) error {
			token, err := daemon.EnsureToken()
			if err != nil {
				return err
			}
			if url {
				scoped, err := scopedBrowserToken(token, security.TokenScopeConsole)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), consoleURL(config.Load(), scoped))
				return nil
			}
			home, err := daemon.Home()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "token: %s\nfile: %s\n", token, filepath.Join(home, "token"))
			return nil
		},
	}
	cmd.Flags().BoolVar(&url, "url", false, "print a local console URL with a scoped token query parameter")
	return cmd
}
