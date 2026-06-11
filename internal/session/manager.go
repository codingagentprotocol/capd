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
	"fmt"
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
	reviving map[string]*reviveCall
}

func NewManager(reg *adapter.Registry, store *Store) *Manager {
	return &Manager{registry: reg, store: store, sessions: make(map[string]*Session), reviving: make(map[string]*reviveCall)}
}

type reviveCall struct {
	wg   sync.WaitGroup
	sess *Session
	err  error
}

// Session pairs an adapter session with the daemon-side event log.
type Session struct {
	ID      string
	AgentID string

	manager *Manager
	inner   adapter.Session
	store   *Store

	mu      sync.Mutex
	buf     []protocol.Event // ring of the last maxBuffer events
	nextSeq uint64
	subs    map[int]*subscriber
	nextSub int
	ended   bool
}

type subscriber struct {
	ch       chan protocol.Event
	overflow chan struct{}
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
		manager: m,
		inner:   inner,
		store:   m.store,
		subs:    make(map[int]*subscriber),
	}
	if m.store != nil {
		if err := m.store.SaveSession(SessionRecord{ID: s.ID, AgentID: agentID, Cwd: opts.Cwd, NativeID: opts.Resume}); err != nil {
			inner.Close()
			return nil, protocol.NewError(protocol.CodeInternalError, "save session: %v", err)
		}
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
	if s, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		if !s.isEnded() {
			return s, nil
		}
		m.mu.Lock()
		if cur, ok := m.sessions[id]; ok && cur == s {
			delete(m.sessions, id)
		}
		m.mu.Unlock()
	} else {
		m.mu.Unlock()
	}
	if m.store == nil {
		return nil, protocol.NewError(protocol.CodeSessionNotFound, "unknown session %q", id)
	}

	m.mu.Lock()
	if call := m.reviving[id]; call != nil {
		m.mu.Unlock()
		call.wg.Wait()
		return call.sess, call.err
	}
	call := &reviveCall{}
	call.wg.Add(1)
	m.reviving[id] = call
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.reviving, id)
		m.mu.Unlock()
		call.wg.Done()
	}()

	rec, err := m.store.LoadSession(id)
	if err != nil {
		call.err = protocol.NewError(protocol.CodeSessionNotFound, "unknown session %q", id)
		return nil, call.err
	}
	if rec.Ended {
		call.err = protocol.NewError(protocol.CodeSessionClosed, "session %q has ended", id)
		return nil, call.err
	}
	a, ok := m.registry.Get(rec.AgentID)
	if !ok {
		call.err = protocol.NewError(protocol.CodeAgentNotFound, "agent %q of stored session is not registered", rec.AgentID)
		return nil, call.err
	}
	inner, err := a.StartSession(ctx, adapter.SessionOpts{Cwd: rec.Cwd, Resume: rec.NativeID})
	if err != nil {
		call.err = protocol.NewError(protocol.CodeAgentUnavailable, "revive %s: %v", rec.AgentID, err)
		return nil, call.err
	}

	s := &Session{
		ID:      rec.ID,
		AgentID: rec.AgentID,
		manager: m,
		inner:   inner,
		store:   m.store,
		subs:    make(map[int]*subscriber),
	}
	if history, err := m.store.LoadRecentEvents(id, maxBuffer); err == nil && len(history) > 0 {
		s.buf = history
	}
	if nextSeq, err := m.store.NextSeq(id); err == nil {
		s.nextSeq = nextSeq
	} else if len(s.buf) > 0 {
		s.nextSeq = s.buf[len(s.buf)-1].Seq + 1
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	go s.pump()
	call.sess = s
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
		manager: m,
		inner:   inner,
		store:   m.store,
		subs:    make(map[int]*subscriber),
	}
	if m.store != nil {
		cwd := ""
		if rec, err := m.store.LoadSession(parent.ID); err == nil {
			cwd = rec.Cwd
		}
		if err := m.store.SaveSession(SessionRecord{ID: s.ID, AgentID: s.AgentID, NativeID: nativeID, Cwd: cwd}); err != nil {
			inner.Close()
			return nil, protocol.NewError(protocol.CodeInternalError, "save forked session: %v", err)
		}
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
		if m.store == nil {
			return protocol.NewError(protocol.CodeSessionNotFound, "unknown session %q", id)
		}
		if _, err := m.store.LoadSession(id); err != nil {
			return protocol.NewError(protocol.CodeSessionNotFound, "unknown session %q", id)
		}
		if err := m.store.MarkEnded(id); err != nil {
			return protocol.NewError(protocol.CodeInternalError, "mark session ended: %v", err)
		}
		return nil
	}
	if m.store != nil {
		if err := m.store.MarkEnded(id); err != nil {
			return protocol.NewError(protocol.CodeInternalError, "mark session ended: %v", err)
		}
	}
	return s.inner.Close()
}

func (m *Manager) forgetEnded(id string, s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.sessions[id]; ok && cur == s {
		delete(m.sessions, id)
	}
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
		persistErr := s.persist(ev)
		s.broadcastLocked(ev)
		if persistErr != nil {
			errEv := protocol.Event{
				SessionID: s.ID,
				Seq:       s.nextSeq,
				Type:      protocol.EventError,
				Data: map[string]any{
					"message":           fmt.Sprintf("persist event %d failed: %v", ev.Seq, persistErr),
					"persistenceFailed": true,
				},
			}
			s.nextSeq++
			s.buf = append(s.buf, errEv)
			if len(s.buf) > maxBuffer {
				s.buf = s.buf[len(s.buf)-maxBuffer:]
			}
			s.broadcastLocked(errEv)
		}
		s.mu.Unlock()
	}

	// Adapter stream ended: tell subscribers and close them out.
	s.mu.Lock()
	s.ended = true
	end := protocol.Event{SessionID: s.ID, Seq: s.nextSeq, Type: protocol.EventSessionEnded}
	s.nextSeq++
	s.buf = append(s.buf, end)
	if err := s.persist(end); err != nil {
		errEv := protocol.Event{
			SessionID: s.ID,
			Seq:       s.nextSeq,
			Type:      protocol.EventError,
			Data: map[string]any{
				"message":           fmt.Sprintf("persist session end failed: %v", err),
				"persistenceFailed": true,
			},
		}
		s.nextSeq++
		s.buf = append(s.buf, errEv)
		s.broadcastLocked(errEv)
	}
	for id, sub := range s.subs {
		select {
		case sub.ch <- end:
		default:
		}
		close(sub.ch)
		delete(s.subs, id)
	}
	s.mu.Unlock()
	if s.manager != nil {
		s.manager.forgetEnded(s.ID, s)
	}
}

// persist writes one event through to the store and keeps the stored
// native conversation id current. Called with s.mu held.
func (s *Session) persist(ev protocol.Event) error {
	if s.store == nil {
		return nil
	}
	if err := s.store.AppendEvent(ev); err != nil {
		return err
	}
	if ev.Type == protocol.EventSessionStarted {
		if nid, ok := ev.Data["nativeSessionId"].(string); ok && nid != "" {
			if err := s.store.SetNativeID(s.ID, nid); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Session) broadcastLocked(ev protocol.Event) {
	for id, sub := range s.subs {
		select {
		case sub.ch <- ev:
		default:
			close(sub.overflow)
			close(sub.ch)
			delete(s.subs, id)
		}
	}
}

// Subscribe returns a channel that replays buffered events from fromSeq and
// then follows the live stream, plus the seq the live stream continues from.
// The returned cancel func must be called when the subscriber goes away.
func (s *Session) Subscribe(fromSeq uint64) (<-chan protocol.Event, uint64, func(), <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var replay []protocol.Event
	if s.store != nil {
		cursor := fromSeq
		for cursor < s.nextSeq {
			events, err := s.store.LoadEvents(s.ID, cursor, 5000)
			if err != nil || len(events) == 0 {
				break
			}
			for _, ev := range events {
				if ev.Seq >= s.nextSeq {
					break
				}
				replay = append(replay, ev)
				cursor = ev.Seq + 1
			}
			if cursor <= events[len(events)-1].Seq {
				break
			}
		}
	} else {
		for _, ev := range s.buf {
			if ev.Seq >= fromSeq {
				replay = append(replay, ev)
			}
		}
	}
	ch := make(chan protocol.Event, len(replay)+subBuffer)
	for _, ev := range replay {
		ch <- ev
	}

	if s.ended {
		close(ch)
		overflow := make(chan struct{})
		return ch, s.nextSeq, func() {}, overflow
	}

	id := s.nextSub
	s.nextSub++
	sub := &subscriber{ch: ch, overflow: make(chan struct{})}
	s.subs[id] = sub
	cancel := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if sub, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(sub.ch)
		}
	}
	return ch, s.nextSeq, cancel, sub.overflow
}

func (s *Session) isEnded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
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
