// Package server exposes the CAP protocol to local clients over
// WebSocket (JSON-RPC 2.0 framing) with token authentication.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/session"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

type Options struct {
	Host        string
	Port        int
	Origins     []string // extra allowed browser origins (localhost always allowed)
	Token       string
	Version     string
	Registry    *adapter.Registry
	Sessions    *session.Manager
	Accounts    *account.Store
	Secrets     secret.Store
	RuntimeRoot string
	// CodexQuotaBaseURL is for tests and private deployments that proxy
	// ChatGPT. Empty uses codexquota.DefaultBaseURL.
	CodexQuotaBaseURL string
	Log               *slog.Logger
}

type Server struct {
	opts       Options
	log        *slog.Logger
	policy     *policyEngine
	accountMu  sync.Mutex
	accountMux map[string]*sync.Mutex
	clients    atomic.Int64
	metrics    *runtimeMetrics
}

func New(opts Options) *Server {
	return &Server{opts: opts, log: opts.Log, policy: newPolicyEngine(), accountMux: map[string]*sync.Mutex{}, metrics: newRuntimeMetrics()}
}

// Run serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/console/", http.StatusFound)
	})
	mux.HandleFunc("GET /console/", s.handleConsole)
	mux.HandleFunc("GET /probe/", s.handleProbe)
	mux.HandleFunc("GET /probe/data", s.handleProbeData)
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
		s.log.Info("capd console", "addr", "http://"+addr+"/console/")
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

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("format") != "json" {
		fmt.Fprintln(w, "ok")
		return
	}
	secretBackend := ""
	if s.opts.Secrets != nil {
		secretBackend = s.opts.Secrets.Backend()
	}
	runtime := s.runtimeHealth()
	writeLocalJSONHeaders(w)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":              true,
		"daemon":          "capd",
		"version":         s.opts.Version,
		"protocolVersion": protocol.Version,
		"secretBackend":   secretBackend,
		"runtime":         runtime,
	})
}

func (s *Server) runtimeHealth() map[string]any {
	runtime := map[string]any{
		"connectedClients": s.clients.Load(),
		"metrics":          s.metrics.snapshot(),
	}
	if s.opts.Sessions == nil {
		return runtime
	}
	sessions := s.opts.Sessions.List(1000)
	active, stored, ended := 0, 0, 0
	for _, sess := range sessions {
		switch sess.State {
		case protocol.SessionStateLive:
			active++
		case protocol.SessionStateStored:
			stored++
		case protocol.SessionStateEnded:
			ended++
		}
	}
	runtime["sessionsListed"] = len(sessions)
	runtime["activeSessions"] = active
	runtime["storedSessions"] = stored
	runtime["endedSessions"] = ended
	return runtime
}
