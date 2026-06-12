package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/config"
)

func newHealthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Check whether the local capd daemon is running",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := config.Load()
			body, err := daemonHealth(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), body)
			return nil
		},
	}
	return cmd
}

func daemonHealth(ctx context.Context, cfg config.Config) (string, error) {
	url := "http://" + daemonAddr(cfg) + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("capd daemon is not healthy at %s (start it with 'capd start'): %w", daemonAddr(cfg), err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	body := strings.TrimSpace(string(data))
	if resp.StatusCode != http.StatusOK || body != "ok" {
		return "", fmt.Errorf("capd daemon is not healthy at %s: status %d body %q", daemonAddr(cfg), resp.StatusCode, body)
	}
	return body, nil
}
