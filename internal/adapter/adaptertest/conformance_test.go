package adaptertest

import (
	"context"
	"sync"
	"testing"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

type scriptedAdapter struct{}

func (scriptedAdapter) ID() string { return "scripted" }

func (scriptedAdapter) Probe(context.Context) (protocol.AgentInfo, error) {
	return protocol.AgentInfo{ID: "scripted", Name: "Scripted", Available: true}, nil
}

func (scriptedAdapter) Capabilities() protocol.AgentCapabilities {
	return protocol.AgentCapabilities{Streaming: true, Resume: true}
}

func (scriptedAdapter) StartSession(context.Context, adapter.SessionOpts) (adapter.Session, error) {
	return &scriptedSession{events: make(chan protocol.Event, 4)}, nil
}

type scriptedSession struct {
	events chan protocol.Event
	once   sync.Once
}

func (s *scriptedSession) Send(context.Context, adapter.Message) error {
	s.events <- protocol.Event{Type: protocol.EventOutputText, Data: map[string]any{"text": "ok"}}
	s.events <- protocol.Event{Type: protocol.EventTaskDone, Data: map[string]any{"ok": true}}
	return nil
}

func (s *scriptedSession) Cancel() {}

func (s *scriptedSession) Events() <-chan protocol.Event { return s.events }

func (s *scriptedSession) Close() error {
	s.once.Do(func() { close(s.events) })
	return nil
}

func TestConformanceHelpers(t *testing.T) {
	a := scriptedAdapter{}
	CheckStaticContract(t, a, StaticContract{
		ID:                 "scripted",
		RequiresCapability: true,
		RequiredCaps:       protocol.AgentCapabilities{Streaming: true, Resume: true},
		ForbiddenCaps:      protocol.AgentCapabilities{Images: true},
	})
	CheckSessionLifecycle(t, a, SessionContract{})
}
