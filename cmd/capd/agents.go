package main

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/daemon"
	"github.com/codingagentprotocol/capd/internal/discovery"
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
	cmd.AddCommand(&cobra.Command{
		Use:   "usage <agent-id>",
		Short: "Account usage and rate limits for one agent (plan, used %, reset times)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, ok := daemon.Registry().Get(args[0])
			if !ok {
				return fmt.Errorf("unknown agent %q", args[0])
			}
			up, ok := a.(adapter.UsageProvider)
			if !ok {
				return fmt.Errorf("agent %q does not report usage", args[0])
			}
			usage, err := up.Usage(cmd.Context())
			if err != nil {
				return err
			}
			out, _ := json.MarshalIndent(usage, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	})
	return cmd
}
