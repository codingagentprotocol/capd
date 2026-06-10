// Package gemini adapts Google's Gemini CLI.
//
// Headless invocation: gemini -p <prompt> (JSON output mode)
package gemini

import (
	"context"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

const ID = "gemini"

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) ID() string { return ID }

func (a *Adapter) Probe(ctx context.Context) (protocol.AgentInfo, error) {
	return adapter.ProbeCLI(ctx, ID, "Gemini CLI", "gemini", "--version")
}

func (a *Adapter) StartSession(_ context.Context, opts adapter.SessionOpts) (adapter.Session, error) {
	return adapter.NewTurnSession(turnConfig, opts), nil
}

var turnConfig = adapter.TurnConfig{
	BuildSpec: buildSpec,
	Translate: translate,
}
