package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/config"
	"github.com/kardianos/service"
	"github.com/spf13/cobra"
)

// newServiceCmd manages capd as a system service (launchd / systemd /
// Windows SCM) so the daemon survives logout and starts on boot.
func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Install or manage capd as a system service",
	}
	for _, action := range []string{"install", "uninstall", "start", "stop", "restart", "status"} {
		action := action
		var secretBackend string
		var host string
		var port int
		var origins []string
		actionCmd := &cobra.Command{
			Use:   action,
			Short: action + " the capd service",
			RunE: func(cmd *cobra.Command, _ []string) error {
				opts, err := serviceOptionsFor(action, cmd, secretBackend, host, port, origins)
				if err != nil {
					return err
				}
				svc, err := newService(opts)
				if err != nil {
					return err
				}
				if action == "status" {
					status, err := svc.Status()
					if err != nil {
						return fmt.Errorf("status: %w (not installed?)", err)
					}
					name := map[service.Status]string{
						service.StatusRunning: "running",
						service.StatusStopped: "stopped",
					}[status]
					if name == "" {
						name = "unknown"
					}
					fmt.Fprintln(cmd.OutOrStdout(), name)
					return nil
				}
				if err := service.Control(svc, action); err != nil {
					return fmt.Errorf("%s: %w", action, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "service %s: ok\n", action)
				if action == "install" {
					fmt.Fprintln(cmd.OutOrStdout(), "start it with: capd service start")
				}
				return nil
			},
		}
		if action == "install" {
			actionCmd.Flags().StringVar(&secretBackend, "secret-backend", "", "SecretStore backend for the installed daemon (file or native; default CAPD_SECRET_BACKEND/file)")
			actionCmd.Flags().StringVar(&host, "host", config.DefaultHost, "address for the installed daemon to bind (default CAPD_HOST/127.0.0.1)")
			actionCmd.Flags().IntVar(&port, "port", config.DefaultPort, "port for the installed daemon to listen on (default CAPD_PORT/7777)")
			actionCmd.Flags().StringSliceVar(&origins, "origins", nil, "extra browser origins for the installed daemon WebSocket (default CAPD_ORIGINS)")
		}
		cmd.AddCommand(actionCmd)
	}
	return cmd
}

type noopProgram struct{}

func (noopProgram) Start(service.Service) error { return nil }
func (noopProgram) Stop(service.Service) error  { return nil }

type serviceOptions struct {
	SecretBackend string
	Host          string
	Port          int
	Origins       []string
}

func serviceOptionsFor(action string, cmd *cobra.Command, secretBackendFlag string, hostFlag string, portFlag int, originsFlag []string) (serviceOptions, error) {
	if action != "install" {
		return serviceOptions{}, nil
	}
	cfg := config.Load()
	backend := cfg.SecretBackend
	if cmd.Flags().Changed("secret-backend") {
		backend = secretBackendFlag
	}
	opts := serviceOptions{}
	if backend != "" {
		backend, err := secret.NormalizeBackend(backend)
		if err != nil {
			return serviceOptions{}, err
		}
		opts.SecretBackend = backend
	}
	if cmd.Flags().Changed("host") {
		cfg.Host = hostFlag
	}
	if cmd.Flags().Changed("port") {
		cfg.Port = portFlag
	}
	if cmd.Flags().Changed("origins") {
		cfg.Origins = originsFlag
	}
	if cfg.Host != config.DefaultHost {
		opts.Host = cfg.Host
	}
	if cfg.Port != config.DefaultPort {
		opts.Port = cfg.Port
	}
	opts.Origins = append([]string(nil), cfg.Origins...)
	return opts, nil
}

func newService(opts serviceOptions) (service.Service, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cfg := serviceConfig(exe, opts)
	return service.New(noopProgram{}, cfg)
}

func serviceConfig(exe string, opts serviceOptions) *service.Config {
	args := []string{"start"}
	if opts.Host != "" {
		args = append(args, "--host", opts.Host)
	}
	if opts.Port != 0 {
		args = append(args, "--port", strconv.Itoa(opts.Port))
	}
	for _, origin := range opts.Origins {
		if origin != "" {
			args = append(args, "--origins", origin)
		}
	}
	if opts.SecretBackend != "" {
		args = append(args, "--secret-backend", opts.SecretBackend)
	}
	cfg := &service.Config{
		Name:        "ai.codingagentprotocol.capd",
		DisplayName: "capd",
		Description: "Coding Agent Protocol daemon — exposes local coding agent CLIs to web & desktop clients.",
		Executable:  exe,
		Arguments:   args,
		Option: service.KeyValue{
			// Run as the logged-in user: agent CLIs need the user's
			// auth state and PATH, never root.
			"UserService": true,
			"RunAtLoad":   true,
			"KeepAlive":   true,
		},
	}
	return cfg
}
