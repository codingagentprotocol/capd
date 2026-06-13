package opencode

import (
	"testing"

	"github.com/codingagentprotocol/capd/internal/adapter/adaptertest"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestOpenCodeAdapterConformanceStaticContract(t *testing.T) {
	adaptertest.CheckStaticContract(t, New(), adaptertest.StaticContract{
		ID:                 ID,
		RequiresCapability: true,
		RequiredCaps: protocol.AgentCapabilities{
			Model:  true,
			Images: true,
			Resume: true,
		},
		ForbiddenCaps: protocol.AgentCapabilities{
			Usage: true,
		},
	})
}
