package main

import (
	"fmt"
	"os"

	"github.com/codingagentprotocol/capd/internal/account/secret"
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
		actionCmd := &cobra.Command{
			Use:   action,
			Short: action + " the capd service",
			RunE: func(cmd *cobra.Command, _ []string) error {
				if action == "install" && cmd.Flags().Changed("secret-backend") {
					backend, err := secret.NormalizeBackend(secretBackend)
					if err != nil {
						return err
					}
					secretBackend = backend
				}
				svc, err := newService(serviceOptions{SecretBackend: secretBackend})
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
