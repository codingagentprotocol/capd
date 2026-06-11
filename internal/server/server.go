// Package server exposes the CAP protocol to local clients over
// WebSocket (JSON-RPC 2.0 framing) with token authentication.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/session"
)

type Options struct {
	Host     string
	Port     int
	Origins  []string // extra allowed browser origins (localhost always allowed)
	Token    string
	Version  string
	Registry *adapter.Registry
	Sessions *session.Manager
	Log      *slog.Logger
}

type Server struct {
	opts   Options
	log    *slog.Logger
	policy *policyEngine
}

func New(opts Options) *Server {
	return &Server{opts: opts, log: opts.Log, policy: newPolicyEngine()}
}

// Run serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /ws", s.handleWS)

	addr := net.JoinHostPort(s.opts.Host, fmt.Sprint(s.opts.Port))
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("capd listening", "addr", "ws://"+addr+"/ws", "version", s.opts.Version)
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
