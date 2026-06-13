package codex

import (
	"testing"

	"github.com/codingagentprotocol/capd/internal/adapter/adaptertest"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestCodexAdapterConformanceStaticContract(t *testing.T) {
	adaptertest.CheckStaticContract(t, New(), adaptertest.StaticContract{
		ID:                 ID,
		RequiresCapability: true,
		RequiredCaps: protocol.AgentCapabilities{
			Model:     true,
			Effort:    true,
			Streaming: true,
			Approvals: true,
			Steer:     true,
			Fork:      true,
			Rollback:  true,
			Review:    true,
			Images:    true,
			Usage:     true,
			Resume:    true,
		},
	})
}
