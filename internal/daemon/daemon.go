// Package daemon is the assembly board: it wires config, adapters, sessions,
// and the server together. This hand-written wiring is the whole DI story —
// every dependency is visible right here.
package daemon

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/adapter/claudecode"
	"github.com/codingagentprotocol/capd/internal/adapter/codex"
	"github.com/codingagentprotocol/capd/internal/adapter/gemini"
	"github.com/codingagentprotocol/capd/internal/config"
	"github.com/codingagentprotocol/capd/internal/server"
	"github.com/codingagentprotocol/capd/internal/session"
)

// Version is stamped by goreleaser via -ldflags.
var Version = "dev"

// Registry returns the adapters compiled into this build.
func Registry() *adapter.Registry {
	return adapter.NewRegistry(
		claudecode.New(),
		codex.New(),
		gemini.New(),
	)
}

// Run assembles and starts the daemon, blocking until ctx is cancelled.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	token, err := EnsureToken()
	if err != nil {
		return err
	}
	home, err := Home()
	if err != nil {
		return err
	}
	store, err := session.OpenStore(filepath.Join(home, "capd.db"))
	if err != nil {
		return err
	}
	defer store.Close()

	reg := Registry()
	sessions := session.NewManager(reg, store)

	srv := server.New(server.Options{
		Host:     cfg.Host,
		Port:     cfg.Port,
		Token:    token,
		Version:  Version,
		Registry: reg,
		Sessions: sessions,
		Log:      log,
	})
	return srv.Run(ctx)
}
