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

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) ID() string { return ID }

func (a *Adapter) Probe(ctx context.Context) (protocol.AgentInfo, error) {
	return adapter.ProbeCLI(ctx, ID, "Codex CLI", "codex", "--version")
}

func (a *Adapter) StartSession(_ context.Context, opts adapter.SessionOpts) (adapter.Session, error) {
	if opts.Resume != "" {
		// A CAP resume maps directly onto codex's native thread id.
		return adapter.NewTurnSessionResumed(turnConfig, opts, opts.Resume), nil
	}
	return adapter.NewTurnSession(turnConfig, opts), nil
}

var turnConfig = adapter.TurnConfig{
	BuildSpec: buildSpec,
	Translate: translate,
}
