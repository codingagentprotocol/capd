package claudecode

import (
	"testing"

	"github.com/codingagentprotocol/capd/internal/adapter/adaptertest"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestClaudeCodeAdapterConformanceStaticContract(t *testing.T) {
	adaptertest.CheckStaticContract(t, New(), adaptertest.StaticContract{
		ID:                 ID,
		RequiresCapability: true,
		RequiredCaps: protocol.AgentCapabilities{
			Model:  true,
			Resume: true,
		},
		ForbiddenCaps: protocol.AgentCapabilities{
			Images: true,
			Usage:  true,
		},
	})
}

func TestClaudeCompatibleAdapterConformanceStaticContract(t *testing.T) {
	adaptertest.CheckStaticContract(t, NewWithCLI("codebuddy", "CodeBuddy", "codebuddy"), adaptertest.StaticContract{
		ID:                 "codebuddy",
		RequiresCapability: true,
		RequiredCaps: protocol.AgentCapabilities{
			Model:  true,
			Resume: true,
		},
	})
}
