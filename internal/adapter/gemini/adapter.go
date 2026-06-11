// Package gemini adapts Google's Gemini CLI.
//
// Headless invocation: gemini -p <prompt> (JSON output mode)
package gemini

import (
	"context"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/proc"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

const ID = "gemini"

// Adapter drives any gemini-cli-family CLI: Gemini itself plus its forks
// (Qwen Code, iFlow), which keep the same headless flags.
type Adapter struct {
	id, name, bin string
}

func New() *Adapter { return NewWithCLI(ID, "Gemini CLI", "gemini") }

// NewWithCLI adapts a gemini-cli fork under its own agent id.
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
	return adapter.NewTurnSession(adapter.TurnConfig{
		BuildSpec: func(opts adapter.SessionOpts, nativeID string, msg adapter.Message) proc.Spec {
			spec := buildSpec(opts, nativeID, msg)
			spec.Bin = a.bin
			return spec
		},
		Translate: translate,
	}, opts), nil
}
