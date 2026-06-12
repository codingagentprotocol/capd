package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/config"
)

func newHealthCmd() *cobra.Command {
	var jsonOut bool
	var requireSecretBackend string
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Check whether the local capd daemon is running",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := config.Load()
			var err error
			requireSecretBackend, err = secret.NormalizeBackend(requireSecretBackend)
			if err != nil {
				return err
			}
			if jsonOut {
				info, err := daemonHealthInfo(cmd.Context(), cfg)
				if err != nil {
					return err
				}
				if err := validateDaemonHealthInfo(info, requireSecretBackend); err != nil {
					return err
				}
				out, _ := json.MarshalIndent(info, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			body, err := daemonHealth(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			if requireSecretBackend != "" {
				info, err := daemonHealthInfo(cmd.Context(), cfg)
				if err != nil {
					return err
				}
				if err := validateDaemonHealthInfo(info, requireSecretBackend); err != nil {
					return err
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), body)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print daemon health as JSON")
	cmd.Flags().StringVar(&requireSecretBackend, "require-secret-backend", "", "fail unless the daemon reports this SecretStore backend (file or native)")
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

type daemonHealthInfoResult struct {
	OK              bool   `json:"ok"`
	Addr            string `json:"addr"`
	Daemon          string `json:"daemon,omitempty"`
	Version         string `json:"version,omitempty"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`
	SecretBackend   string `json:"secretBackend,omitempty"`
}

func daemonHealthInfo(ctx context.Context, cfg config.Config) (daemonHealthInfoResult, error) {
	u := url.URL{
		Scheme: "http",
		Host:   daemonAddr(cfg),
		Path:   "/healthz",
	}
	q := u.Query()
	q.Set("format", "json")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return daemonHealthInfoResult{}, err
	}
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return daemonHealthInfoResult{}, fmt.Errorf("capd daemon is not healthy at %s (start it with 'capd start'): %w", daemonAddr(cfg), err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	body := strings.TrimSpace(string(data))
	if resp.StatusCode != http.StatusOK {
		return daemonHealthInfoResult{}, fmt.Errorf("capd daemon is not healthy at %s: status %d body %q", daemonAddr(cfg), resp.StatusCode, body)
	}
	var info daemonHealthInfoResult
	if err := json.Unmarshal(data, &info); err != nil {
		if body != "ok" {
			return daemonHealthInfoResult{}, fmt.Errorf("capd daemon health JSON is invalid at %s: %w", daemonAddr(cfg), err)
		}
		info.OK = true
	}
	if !info.OK {
		return daemonHealthInfoResult{}, fmt.Errorf("capd daemon is not healthy at %s: %s", daemonAddr(cfg), body)
	}
	info.Addr = daemonAddr(cfg)
	return info, nil
}

func validateDaemonHealthInfo(info daemonHealthInfoResult, requireSecretBackend string) error {
	if requireSecretBackend == "" {
		return nil
	}
	if info.SecretBackend == "" {
		return fmt.Errorf("daemon health does not report secret backend; restart or upgrade capd before requiring %q", requireSecretBackend)
	}
	if info.SecretBackend != requireSecretBackend {
		return fmt.Errorf("daemon secret backend = %q, want %q", info.SecretBackend, requireSecretBackend)
	}
	return nil
}
