package gemini

import (
	"testing"

	"github.com/codingagentprotocol/capd/internal/adapter/adaptertest"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestGeminiAdapterConformanceStaticContract(t *testing.T) {
	adaptertest.CheckStaticContract(t, New(), adaptertest.StaticContract{
		ID:                 ID,
		RequiresCapability: true,
		RequiredCaps: protocol.AgentCapabilities{
			Model: true,
		},
		ForbiddenCaps: protocol.AgentCapabilities{
			Images: true,
			Resume: true,
			Usage:  true,
		},
	})
}

func TestGeminiCompatibleAdapterConformanceStaticContract(t *testing.T) {
	for _, tc := range []struct {
		id, name, bin string
	}{
		{"qwen-code", "Qwen Code", "qwen"},
		{"iflow", "iFlow", "iflow"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			adaptertest.CheckStaticContract(t, NewWithCLI(tc.id, tc.name, tc.bin), adaptertest.StaticContract{
				ID:                 tc.id,
				RequiresCapability: true,
				RequiredCaps: protocol.AgentCapabilities{
					Model: true,
				},
			})
		})
	}
}
