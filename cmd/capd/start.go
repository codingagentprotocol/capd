package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/config"
	"github.com/codingagentprotocol/capd/internal/daemon"
)

func newStartCmd() *cobra.Command {
	var host string
	var port int
	var origins []string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the daemon in the foreground",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := config.Load()
			if cmd.Flags().Changed("host") {
				cfg.Host = host
			}
			if cmd.Flags().Changed("port") {
				cfg.Port = port
			}
			if len(origins) > 0 {
				cfg.Origins = append(cfg.Origins, origins...)
			}

			log := slog.New(slog.NewTextHandler(os.Stderr, nil))
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return daemon.Run(ctx, cfg, log)
		},
	}
	cmd.Flags().StringVar(&host, "host", config.DefaultHost, "address to bind (keep it local)")
	cmd.Flags().IntVar(&port, "port", config.DefaultPort, "port to listen on")
	cmd.Flags().StringSliceVar(&origins, "origins", nil, "extra browser origins allowed for WebSocket (localhost always allowed)")
	return cmd
}
