// Package codex adapts OpenAI's Codex CLI.
//
// Headless invocation: codex exec --json <prompt>
package codex

import (
	"context"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

const ID = "codex"

type Adapter struct {
	appServer appServer
}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) ID() string { return ID }

func (a *Adapter) Probe(ctx context.Context) (protocol.AgentInfo, error) {
	return adapter.ProbeCLI(ctx, ID, "Codex CLI", "codex", "--version")
}

// StartSession prefers app-server mode — codex's own desktop-app engine,
// which unlocks streaming deltas, interactive approvals, and turn steering.
// If the app-server cannot start (older codex builds), it degrades to the
// spawn-per-turn exec mode.
func (a *Adapter) StartSession(ctx context.Context, opts adapter.SessionOpts) (adapter.Session, error) {
	s, err := a.appServer.startAppSession(ctx, opts)
	if err == nil {
		return s, nil
	}
	if opts.Resume != "" {
		return adapter.NewTurnSessionResumed(turnConfig, opts, opts.Resume), nil
	}
	return adapter.NewTurnSession(turnConfig, opts), nil
}

var turnConfig = adapter.TurnConfig{
	BuildSpec:      buildSpec,
	Translate:      translate,
	SupportsImages: true,
}
