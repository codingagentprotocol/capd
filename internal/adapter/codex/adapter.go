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

func (a *Adapter) StartSession(ctx context.Context, opts adapter.SessionOpts) (adapter.Session, error) {
	return nil, adapter.ErrNotImplemented
}
