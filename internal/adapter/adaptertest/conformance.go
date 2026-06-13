// Package adaptertest contains reusable conformance checks for adapter
// implementations. The checks avoid real agent CLIs by default; adapter
// packages opt into only the contracts they can prove deterministically.
package adaptertest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

type StaticContract struct {
	ID                 string
	RequiredCaps       protocol.AgentCapabilities
	ForbiddenCaps      protocol.AgentCapabilities
	RequiresCapability bool
}

func CheckStaticContract(t testing.TB, a adapter.Adapter, c StaticContract) {
	t.Helper()
	if a == nil {
		t.Fatal("adapter is nil")
	}
	if got := strings.TrimSpace(a.ID()); got == "" || got != a.ID() {
		t.Fatalf("adapter ID = %q, want non-empty trimmed id", a.ID())
	}
	if c.ID != "" && a.ID() != c.ID {
		t.Fatalf("adapter ID = %q, want %q", a.ID(), c.ID)
	}
	cp, ok := a.(adapter.CapabilityProvider)
	if c.RequiresCapability && !ok {
		t.Fatalf("adapter %q does not implement CapabilityProvider", a.ID())
	}
	if !ok {
		return
	}
	got := cp.Capabilities()
	assertCaps(t, a.ID(), got, c.RequiredCaps, true)
	assertCaps(t, a.ID(), got, c.ForbiddenCaps, false)
}

type SessionContract struct {
	Message       adapter.Message
	WantEventType protocol.EventType
	Timeout       time.Duration
}

func CheckSessionLifecycle(t testing.TB, a adapter.Adapter, c SessionContract) {
	t.Helper()
	if c.Timeout == 0 {
		c.Timeout = 2 * time.Second
	}
	if c.Message.Prompt == "" {
		c.Message.Prompt = "hello"
	}
	if c.WantEventType == "" {
		c.WantEventType = protocol.EventTaskDone
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	sess, err := a.StartSession(ctx, adapter.SessionOpts{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if sess == nil {
		t.Fatal("StartSession returned nil session")
	}
	if sess.Events() == nil {
		t.Fatal("session Events channel is nil")
	}
	if err := sess.Send(ctx, c.Message); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var sawWanted bool
	for !sawWanted {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatalf("events closed before %s", c.WantEventType)
			}
			if ev.Type == c.WantEventType {
				sawWanted = true
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", c.WantEventType)
		}
	}
	sess.Cancel()
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case _, ok := <-sess.Events():
		if ok {
			t.Fatal("events channel still open after Close")
		}
	case <-time.After(c.Timeout):
		t.Fatal("events channel did not close after Close")
	}
}

func assertCaps(t testing.TB, id string, got, want protocol.AgentCapabilities, wantValue bool) {
	t.Helper()
	for _, cap := range []struct {
		name  string
		got   bool
		want  bool
		check bool
	}{
		{"model", got.Model, want.Model, want.Model},
		{"effort", got.Effort, want.Effort, want.Effort},
		{"streaming", got.Streaming, want.Streaming, want.Streaming},
		{"approvals", got.Approvals, want.Approvals, want.Approvals},
		{"steer", got.Steer, want.Steer, want.Steer},
		{"fork", got.Fork, want.Fork, want.Fork},
		{"rollback", got.Rollback, want.Rollback, want.Rollback},
		{"review", got.Review, want.Review, want.Review},
		{"images", got.Images, want.Images, want.Images},
		{"usage", got.Usage, want.Usage, want.Usage},
		{"resume", got.Resume, want.Resume, want.Resume},
	} {
		if !cap.check {
			continue
		}
		if cap.got != wantValue {
			t.Fatalf("adapter %q capability %s = %t, want %t", id, cap.name, cap.got, wantValue)
		}
	}
}
