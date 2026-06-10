// capd is the Coding Agent Protocol daemon: it discovers coding agent CLIs
// installed on this machine and exposes them to web and desktop clients
// over a unified protocol.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/daemon"
)

func main() {
	root := &cobra.Command{
		Use:           "capd",
		Short:         "Coding Agent Protocol daemon",
		Long:          "capd discovers local coding agent CLIs (Claude Code, Codex, Gemini, ...) and exposes them to web & desktop clients over the Coding Agent Protocol.",
		Version:       daemon.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newStartCmd(), newAgentsCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "capd:", err)
		os.Exit(1)
	}
}
