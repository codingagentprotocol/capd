// Package adapter defines the boundary between capd and individual coding
// agent CLIs. The interface only passes serializable protocol types, so a
// future out-of-process adapter (any language, CAP over stdio) can implement
// it without changes on the daemon side.
package adapter

import (
	"context"
	"errors"

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

var ErrNotImplemented = errors.New("adapter: not implemented yet")

// Adapter wraps one coding agent CLI.
type Adapter interface {
	// ID returns the stable agent identifier, e.g. "claude-code".
	ID() string
	// Probe checks whether the CLI is installed and reports its version.
	// A missing CLI is not an error: it returns AgentInfo{Available: false}.
	Probe(ctx context.Context) (protocol.AgentInfo, error)
	// StartSession spawns the agent and returns a live session.
	StartSession(ctx context.Context, opts SessionOpts) (Session, error)
}

type SessionOpts struct {
	Cwd    string
	Resume string // agent-native session id to resume, if supported
}

// Session is one running conversation with an agent.
type Session interface {
	// Send delivers a prompt or control message into the session.
	Send(ctx context.Context, prompt string) error
	// Events streams unified protocol events until the session ends.
	Events() <-chan protocol.Event
	Close() error
}
