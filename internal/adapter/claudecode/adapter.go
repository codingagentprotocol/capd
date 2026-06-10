// Package claudecode adapts Anthropic's Claude Code CLI.
//
// Headless invocation: claude -p <prompt> --output-format stream-json
// translate.go (next milestone) maps its stream-json events to CAP events.
package claudecode

import (
	"context"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

const ID = "claude-code"

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) ID() string { return ID }

func (a *Adapter) Probe(ctx context.Context) (protocol.AgentInfo, error) {
	return adapter.ProbeCLI(ctx, ID, "Claude Code", "claude", "--version")
}

func (a *Adapter) StartSession(ctx context.Context, opts adapter.SessionOpts) (adapter.Session, error) {
	return nil, adapter.ErrNotImplemented
}
