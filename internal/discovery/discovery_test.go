package discovery

import (
	"context"
	"testing"

	"github.com/codingagentprotocol/capd/internal/adapter"
)

func TestDiscoverProbesAllInStableOrder(t *testing.T) {
	reg := adapter.NewRegistry(
		adapter.NewPendingCLI("zeta", "Z", "no-such-bin-z"),
		adapter.NewPendingCLI("alpha", "A", "sh"), // sh exists everywhere
	)
	infos := Discover(context.Background(), reg)
	if len(infos) != 2 {
		t.Fatalf("got %d", len(infos))
	}
	if infos[0].ID != "alpha" || infos[1].ID != "zeta" {
		t.Fatalf("order: %s, %s", infos[0].ID, infos[1].ID)
	}
	if !infos[0].Available || infos[1].Available {
		t.Fatalf("availability: %+v", infos)
	}
}
