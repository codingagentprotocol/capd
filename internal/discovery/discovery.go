// Package discovery probes the registered adapters concurrently and reports
// which coding agent CLIs exist on this machine.
package discovery

import (
	"context"
	"sync"
	"time"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

const probeTimeout = 15 * time.Second

// Discover probes every adapter in the registry. Order matches Registry.All.
func Discover(ctx context.Context, reg *adapter.Registry) []protocol.AgentInfo {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	adapters := reg.All()
	infos := make([]protocol.AgentInfo, len(adapters))
	var wg sync.WaitGroup
	for i, a := range adapters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := a.Probe(ctx)
			if err != nil {
				info = protocol.AgentInfo{ID: a.ID(), Available: false}
			}
			if cp, ok := a.(adapter.CapabilityProvider); ok {
				info.Capabilities = cp.Capabilities()
			}
			infos[i] = info
		}()
	}
	wg.Wait()
	return infos
}
