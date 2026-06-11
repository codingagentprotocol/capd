// Package claudecode adapts Anthropic's Claude Code CLI.
//
// Headless invocation: claude -p <prompt> --output-format stream-json
// translate.go (next milestone) maps its stream-json events to CAP events.
package claudecode

import (
	"context"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/proc"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

const ID = "claude-code"

// Adapter drives Claude Code and CLIs that clone its headless interface
// (-p / --output-format stream-json / --resume), e.g. Tencent CodeBuddy.
type Adapter struct {
	id, name, bin string
}

func New() *Adapter { return NewWithCLI(ID, "Claude Code", "claude") }

// NewWithCLI adapts a claude-code-compatible CLI under its own agent id.
func NewWithCLI(id, name, bin string) *Adapter {
	return &Adapter{id: id, name: name, bin: bin}
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) Probe(ctx context.Context) (protocol.AgentInfo, error) {
	return adapter.ProbeCLI(ctx, a.id, a.name, a.bin, "--version")
}

func (a *Adapter) StartSession(_ context.Context, opts adapter.SessionOpts) (adapter.Session, error) {
	if err := adapter.RequireBin(a.id, a.bin); err != nil {
		return nil, err
	}
	cfg := adapter.TurnConfig{
		BuildSpec: func(opts adapter.SessionOpts, nativeID, prompt string) proc.Spec {
			spec := buildSpec(opts, nativeID, prompt)
			spec.Bin = a.bin
			return spec
		},
		Translate: translate,
	}
	if opts.Resume != "" {
		return adapter.NewTurnSessionResumed(cfg, opts, opts.Resume), nil
	}
	return adapter.NewTurnSession(cfg, opts), nil
}
