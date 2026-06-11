// Package session owns the session registry: creating sessions on top of
// adapters, stamping events with monotonically increasing sequence numbers,
// buffering them for replay, and fanning them out to subscribers. A dropped
// client connection never kills a session — clients re-attach with
// session/attach and the seq they last saw.
//
// Sessions survive daemon restarts: identity (agent + native conversation id)
// and the event log are persisted, and Resolve revives a stored session on
// first touch, resuming the agent-native conversation.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// maxBuffer bounds the per-session in-memory replay buffer; deeper history
// stays in the store.
const maxBuffer = 4096

// subBuffer is each subscriber's channel capacity. A subscriber that stalls
// past it loses events rather than stalling the session.
const subBuffer = 256

// Manager tracks live sessions.
type Manager struct {
	registry *adapter.Registry
	store    *Store // nil means in-memory only (tests)

	mu       sync.Mutex
	sessions map[string]*Session
}

func NewManager(reg *adapter.Registry, store *Store) *Manager {
	return &Manager{registry: reg, store: store, sessions: make(map[string]*Session)}
}

// Session pairs an adapter session with the daemon-side event log.
type Session struct {
	ID      string
	AgentID string

	inner adapter.Session
	store *Store

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
		store:   m.store,
		subs:    make(map[int]chan protocol.Event),
	}
	if m.store != nil {
		m.store.SaveSession(SessionRecord{ID: s.ID, AgentID: agentID, Cwd: opts.Cwd, NativeID: opts.Resume})
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()

	go s.pump()
	return s, nil
}

// Resolve returns a live session, reviving it from the store if this daemon
// process has not touched it yet. Reviving resumes the agent-native
// conversation and reloads recent history into the replay buffer.
func (m *Manager) Resolve(ctx context.Context, id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		return s, nil
	}
	if m.store == nil {
		return nil, protocol.NewError(protocol.CodeSessionNotFound, "unknown session %q", id)
	}

	rec, err := m.store.LoadSession(id)
	if err != nil {
		return nil, protocol.NewError(protocol.CodeSessionNotFound, "unknown session %q", id)
	}
	if rec.Ended {
		return nil, protocol.NewError(protocol.CodeSessionClosed, "session %q has ended", id)
	}
	a, ok := m.registry.Get(rec.AgentID)
	if !ok {
		return nil, protocol.NewError(protocol.CodeAgentNotFound, "agent %q of stored session is not registered", rec.AgentID)
	}
	inner, err := a.StartSession(ctx, adapter.SessionOpts{Cwd: rec.Cwd, Resume: rec.NativeID})
	if err != nil {
		return nil, protocol.NewError(protocol.CodeAgentUnavailable, "revive %s: %v", rec.AgentID, err)
	}

	s := &Session{
		ID:      rec.ID,
		AgentID: rec.AgentID,
		inner:   inner,
		store:   m.store,
		subs:    make(map[int]chan protocol.Event),
	}
	if history, err := m.store.LoadEvents(id, 0, maxBuffer); err == nil && len(history) > 0 {
		s.buf = history
		s.nextSeq = history[len(history)-1].Seq + 1
	}
	m.sessions[s.ID] = s
	go s.pump()
	return s, nil
}

// List reports sessions and their liveness, newest first: live ones running
// in this process, stored ones that revive on touch, and ended ones.
func (m *Manager) List(limit int) []protocol.SessionInfo {
	m.mu.Lock()
	live := make(map[string]bool, len(m.sessions))
	for id := range m.sessions {
		live[id] = true
	}
	m.mu.Unlock()

	var out []protocol.SessionInfo
	seen := make(map[string]bool)
	if m.store != nil {
		if recs, err := m.store.LoadSessions(limit); err == nil {
			for _, rec := range recs {
				state := protocol.SessionStateStored
				if rec.Ended {
					state = protocol.SessionStateEnded
				}
				if live[rec.ID] {
					state = protocol.SessionStateLive
				}
				out = append(out, protocol.SessionInfo{
					SessionID: rec.ID, AgentID: rec.AgentID, Cwd: rec.Cwd,
					State: state, CreatedAt: rec.CreatedAt,
				})
				seen[rec.ID] = true
			}
		}
	}
	// In-memory sessions missing from the store (store-less test setups).
	m.mu.Lock()
	for id, s := range m.sessions {
		if !seen[id] {
			out = append(out, protocol.SessionInfo{SessionID: id, AgentID: s.AgentID, State: protocol.SessionStateLive})
		}
	}
	m.mu.Unlock()
	return out
}

// History returns stored events without touching the session's liveness —
// pure read, no revival.
func (m *Manager) History(id string, fromSeq uint64, limit int) ([]protocol.Event, error) {
	if m.store != nil {
		evs, err := m.store.LoadEvents(id, fromSeq, limit)
		if err == nil && len(evs) > 0 {
			return evs, nil
		}
		// Unknown id should error rather than return an empty page.
		if _, lerr := m.store.LoadSession(id); lerr != nil {
			m.mu.Lock()
			_, inMem := m.sessions[id]
			m.mu.Unlock()
			if !inMem {
				return nil, protocol.NewError(protocol.CodeSessionNotFound, "unknown session %q", id)
			}
		}
		return evs, err
	}

	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return nil, protocol.NewError(protocol.CodeSessionNotFound, "unknown session %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []protocol.Event
	for _, ev := range s.buf {
		if ev.Seq >= fromSeq && len(out) < limit {
			out = append(out, ev)
		}
	}
	return out, nil
}

// Fork branches an existing session into a new, independent one that shares
// conversation history up to this point.
func (m *Manager) Fork(ctx context.Context, id string) (*Session, error) {
	parent, err := m.Resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	forker, ok := parent.inner.(adapter.Forker)
	if !ok {
		return nil, protocol.NewError(protocol.CodeMethodNotFound, "agent %q does not support forking", parent.AgentID)
	}
	inner, nativeID, err := forker.Fork(ctx)
	if err != nil {
		return nil, protocol.NewError(protocol.CodeAgentUnavailable, "fork: %v", err)
	}

	s := &Session{
		ID:      newID(),
		AgentID: parent.AgentID,
		inner:   inner,
		store:   m.store,
		subs:    make(map[int]chan protocol.Event),
	}
	if m.store != nil {
		cwd := ""
		if rec, err := m.store.LoadSession(parent.ID); err == nil {
			cwd = rec.Cwd
		}
		m.store.SaveSession(SessionRecord{ID: s.ID, AgentID: s.AgentID, NativeID: nativeID, Cwd: cwd})
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	go s.pump()
	return s, nil
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
	if m.store != nil {
		m.store.MarkEnded(id)
	}
	return s.inner.Close()
}

// pump stamps, persists, and stores every adapter event, then broadcasts it.
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
		s.persist(ev)
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
	s.persist(end)
	for id, ch := range s.subs {
		select {
		case ch <- end:
		default:
		}
		close(ch)
		delete(s.subs, id)
	}
}

// persist writes one event through to the store and keeps the stored
// native conversation id current. Called with s.mu held.
func (s *Session) persist(ev protocol.Event) {
	if s.store == nil {
		return
	}
	s.store.AppendEvent(ev)
	if ev.Type == protocol.EventSessionStarted {
		if nid, ok := ev.Data["nativeSessionId"].(string); ok && nid != "" {
			s.store.SetNativeID(s.ID, nid)
		}
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
func (s *Session) Send(ctx context.Context, msg adapter.Message) error {
	return s.inner.Send(ctx, msg)
}

// Rollback drops the last numTurns turns, when the agent supports it.
func (s *Session) Rollback(ctx context.Context, numTurns int) error {
	if rb, ok := s.inner.(adapter.Rollbacker); ok {
		return rb.Rollback(ctx, numTurns)
	}
	return protocol.NewError(protocol.CodeMethodNotFound, "agent %q does not support rollback", s.AgentID)
}

// Review starts a code-review turn, when the agent supports it.
func (s *Session) Review(ctx context.Context, target protocol.ReviewTarget) error {
	if rv, ok := s.inner.(adapter.Reviewer); ok {
		return rv.Review(ctx, target)
	}
	return protocol.NewError(protocol.CodeMethodNotFound, "agent %q does not support review", s.AgentID)
}

// Cancel interrupts the running turn.
func (s *Session) Cancel() { s.inner.Cancel() }

// Steer injects guidance into the running turn, when the agent supports it.
func (s *Session) Steer(ctx context.Context, prompt string) error {
	if st, ok := s.inner.(adapter.Steerer); ok {
		return st.Steer(ctx, prompt)
	}
	return protocol.NewError(protocol.CodeMethodNotFound, "agent %q does not support steering", s.AgentID)
}

// Approve answers a pending approval, when the agent supports it.
func (s *Session) Approve(ctx context.Context, approvalID, decision string) error {
	if ap, ok := s.inner.(adapter.Approver); ok {
		return ap.Approve(ctx, approvalID, decision)
	}
	return protocol.NewError(protocol.CodeMethodNotFound, "agent %q does not support approvals", s.AgentID)
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "s_" + hex.EncodeToString(b)
}
