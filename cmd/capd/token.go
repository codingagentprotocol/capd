package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/daemon"
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
				fmt.Fprintf(cmd.OutOrStdout(), "http://127.0.0.1:7777/console/?token=%s\n", token)
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
	cmd.Flags().BoolVar(&url, "url", false, "print a local console URL with the token query parameter")
	return cmd
}
