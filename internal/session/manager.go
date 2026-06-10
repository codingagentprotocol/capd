// Package session owns the session registry: creating sessions on top of
// adapters, fanning events out to subscribed connections, and surviving
// client disconnects (a dropped WebSocket never kills an agent session).
//
// Persistence (SQLite-backed history and resume) lands in a later milestone.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// Manager tracks live sessions.
type Manager struct {
	registry *adapter.Registry

	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewManager(reg *adapter.Registry) *Manager {
	return &Manager{registry: reg, sessions: make(map[string]*Session)}
}

// Session pairs an adapter session with a capd-assigned ID.
type Session struct {
	ID      string
	AgentID string
	inner   adapter.Session
}

// Create starts a new agent session.
func (m *Manager) Create(ctx context.Context, agentID string, opts adapter.SessionOpts) (*Session, error) {
	a, ok := m.registry.Get(agentID)
	if !ok {
		return nil, protocol.NewError(protocol.CodeAgentNotFound, "unknown agent %q", agentID)
	}
	inner, err := a.StartSession(ctx, opts)
	if err != nil {
		return nil, protocol.NewError(protocol.CodeAgentUnavailable, "start %s: %v", agentID, err)
	}
	s := &Session{ID: newID(), AgentID: agentID, inner: inner}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	return s, nil
}

func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "s_" + hex.EncodeToString(b)
}
