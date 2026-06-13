package cursoragent

import (
	"testing"

	"github.com/codingagentprotocol/capd/internal/adapter/adaptertest"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestCursorAgentAdapterConformanceStaticContract(t *testing.T) {
	adaptertest.CheckStaticContract(t, New(), adaptertest.StaticContract{
		ID:                 ID,
		RequiresCapability: true,
		RequiredCaps: protocol.AgentCapabilities{
			Resume: true,
		},
		ForbiddenCaps: protocol.AgentCapabilities{
			Images: true,
			Usage:  true,
		},
	})
}
