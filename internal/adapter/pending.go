package adapter

import (
	"context"
	"fmt"

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// PendingCLI is an adapter for a CLI capd can discover (probe, version) but
// whose session wiring has not been calibrated against real output yet.
// StartSession fails with an honest message instead of spawning a TUI that
// would hang headless.
type PendingCLI struct {
	id, name, bin string
	versionArgs   []string
}

func NewPendingCLI(id, name, bin string, versionArgs ...string) *PendingCLI {
	return &PendingCLI{id: id, name: name, bin: bin, versionArgs: versionArgs}
}

func (p *PendingCLI) ID() string { return p.id }

func (p *PendingCLI) Probe(ctx context.Context) (protocol.AgentInfo, error) {
	return ProbeCLI(ctx, p.id, p.name, p.bin, p.versionArgs...)
}

func (p *PendingCLI) Capabilities() protocol.AgentCapabilities {
	return protocol.AgentCapabilities{}
}

func (p *PendingCLI) StartSession(context.Context, SessionOpts) (Session, error) {
	return nil, fmt.Errorf("adapter %q is discovery-only for now: its headless interface has not been calibrated yet", p.id)
}
