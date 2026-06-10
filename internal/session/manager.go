// Package session owns the session registry: creating sessions on top of
// adapters, stamping events with monotonically increasing sequence numbers,
// buffering them for replay, and fanning them out to subscribers. A dropped
// client connection never kills a session — clients re-attach with
// session/attach and the seq they last saw.
//
// Persistence across daemon restarts (SQLite) lands in a later milestone.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// maxBuffer bounds the per-session replay buffer; older events are dropped.
const maxBuffer = 4096

// subBuffer is each subscriber's channel capacity. A subscriber that stalls
// past it loses events rather than stalling the session.
const subBuffer = 256

// Manager tracks live sessions.
type Manager struct {
	registry *adapter.Registry

	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewManager(reg *adapter.Registry) *Manager {
	return &Manager{registry: reg, sessions: make(map[string]*Session)}
}

// Session pairs an adapter session with the daemon-side event log.
type Session struct {
	ID      string
	AgentID string

	inner adapter.Session

	mu      sync.Mutex
	buf     []protocol.Event // ring of the last maxBuffer events
	nextSeq uint64
	subs    map[int]chan protocol.Event
	nextSub int
	ended   bool
}

// Create starts a new agent session and begins pumping its events.
func (m *Manager) Create(ctx context.Context, agentID string, opts adapter.SessionOpts) (*Session, error) {
	a, ok := m.registry.Get(agentID)
	if !ok {
		return nil, protocol.NewError(protocol.CodeAgentNotFound, "unknown agent %q", agentID)
	}
	inner, err := a.StartSession(ctx, opts)
	if err != nil {
		return nil, protocol.NewError(protocol.CodeAgentUnavailable, "start %s: %v", agentID, err)
	}
	s := &Session{
		ID:      newID(),
		AgentID: agentID,
		inner:   inner,
		subs:    make(map[int]chan protocol.Event),
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()

	go s.pump()
	return s, nil
}

func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

// Close terminates a session and removes it from the registry.
func (m *Manager) Close(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if !ok {
		return protocol.NewError(protocol.CodeSessionNotFound, "unknown session %q", id)
	}
	return s.inner.Close()
}

// pump stamps and stores every adapter event, then broadcasts it.
func (s *Session) pump() {
	for ev := range s.inner.Events() {
		s.mu.Lock()
		ev.SessionID = s.ID
		ev.Seq = s.nextSeq
		s.nextSeq++
		s.buf = append(s.buf, ev)
		if len(s.buf) > maxBuffer {
			s.buf = s.buf[len(s.buf)-maxBuffer:]
		}
		for _, ch := range s.subs {
			select {
			case ch <- ev:
			default: // slow subscriber: drop rather than stall the session
			}
		}
		s.mu.Unlock()
	}

	// Adapter stream ended: tell subscribers and close them out.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
	end := protocol.Event{SessionID: s.ID, Seq: s.nextSeq, Type: protocol.EventSessionEnded}
	s.nextSeq++
	s.buf = append(s.buf, end)
	for id, ch := range s.subs {
		select {
		case ch <- end:
		default:
		}
		close(ch)
		delete(s.subs, id)
	}
}

// Subscribe returns a channel that replays buffered events from fromSeq and
// then follows the live stream, plus the seq the live stream continues from.
// The returned cancel func must be called when the subscriber goes away.
func (s *Session) Subscribe(fromSeq uint64) (<-chan protocol.Event, uint64, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var replay []protocol.Event
	for _, ev := range s.buf {
		if ev.Seq >= fromSeq {
			replay = append(replay, ev)
		}
	}
	ch := make(chan protocol.Event, len(replay)+subBuffer)
	for _, ev := range replay {
		ch <- ev
	}

	if s.ended {
		close(ch)
		return ch, s.nextSeq, func() {}
	}

	id := s.nextSub
	s.nextSub++
	s.subs[id] = ch
	cancel := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if ch, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(ch)
		}
	}
	return ch, s.nextSeq, cancel
}

// Send starts a new turn.
func (s *Session) Send(ctx context.Context, prompt string) error {
	return s.inner.Send(ctx, prompt)
}

// Cancel interrupts the running turn.
func (s *Session) Cancel() { s.inner.Cancel() }

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "s_" + hex.EncodeToString(b)
}
