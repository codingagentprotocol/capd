package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/config"
	"github.com/codingagentprotocol/capd/internal/daemon"
)

func newConsoleCmd() *cobra.Command {
	var probe bool
	var printURL bool
	cmd := &cobra.Command{
		Use:   "console",
		Short: "Open the local web console or probe",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := config.Load()
			if _, err := daemonHealth(cmd.Context(), cfg); err != nil {
				return err
			}
			token, err := readDaemonToken()
			if err != nil {
				return err
			}
			target := consoleURL(cfg, token)
			label := "console"
			if probe {
				target = probeURL(cfg, token)
				label = "probe"
			}
			if printURL {
				fmt.Fprintln(cmd.OutOrStdout(), target)
				return nil
			}
			if err := openBrowser(target); err != nil {
				return fmt.Errorf("open %s: %w", label, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "opened capd %s at http://%s/%s\n", label, daemonAddr(cfg), labelPath(probe))
			return nil
		},
	}
	cmd.Flags().BoolVar(&probe, "probe", false, "open the lightweight data probe instead of the full console")
	cmd.Flags().BoolVar(&printURL, "url", false, "print the local URL with token instead of opening it")
	return cmd
}

func readDaemonToken() (string, error) {
	home, err := daemon.Home()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(home, "token"))
	if err != nil {
		return "", fmt.Errorf("no daemon token (is capd started?): %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("daemon token is empty")
	}
	return token, nil
}

func labelPath(probe bool) string {
	if probe {
		return "probe/"
	}
	return "console/"
}

func openBrowser(target string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
		args = []string{target}
	case "windows":
		name = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", target}
	default:
		name = "xdg-open"
		args = []string{target}
	}
	return exec.Command(name, args...).Start()
}
