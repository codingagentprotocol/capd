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
	// PermissionMode is one of the protocol.Permission* constants; each
	// adapter maps it onto its CLI's native flags, never onto config files.
	PermissionMode string
	Model          string // agent-native model id; empty = agent default
	Effort         string // agent-native reasoning effort; empty = default
	Env            []string
}

// Message is one user turn: a prompt plus optional attachments. Adapters
// that cannot handle an attachment type must reject the Send, not drop data.
type Message struct {
	Prompt string
	Images []protocol.Attachment
}

// Forker is an optional session capability: branch the conversation into an
// independent session that shares history up to this point. Returns the new
// inner session and its agent-native id (for persistence).
type Forker interface {
	Fork(ctx context.Context) (Session, string, error)
}

// Rollbacker is an optional session capability: drop the last numTurns turns.
type Rollbacker interface {
	Rollback(ctx context.Context, numTurns int) error
}

// Reviewer is an optional session capability: run a code-review turn against
// the target; results stream as ordinary session events.
type Reviewer interface {
	Review(ctx context.Context, target protocol.ReviewTarget) error
}

// Steerer is an optional session capability: inject guidance into the
// running turn without interrupting it.
type Steerer interface {
	Steer(ctx context.Context, prompt string) error
}

// Approver is an optional session capability: answer a pending
// approval.needed event. decision is a protocol.Decision* constant.
type Approver interface {
	Approve(ctx context.Context, approvalID, decision string) error
}

// UsageProvider is an optional adapter capability: account-level usage and
// rate-limit data (plan, used percent, window reset times). Exposed over the
// protocol as agents/usage; adapters without it answer "not supported".
type UsageProvider interface {
	Usage(ctx context.Context) (map[string]any, error)
}

// CapabilityProvider describes stable daemon-known adapter behavior. It lets
// clients and schedulers choose agents without probing by trial and error.
type CapabilityProvider interface {
	Capabilities() protocol.AgentCapabilities
}

// Session is one running conversation with an agent.
type Session interface {
	// Send starts a new turn. It returns an error if a turn is already
	// running or the message carries attachments the agent cannot handle.
	Send(ctx context.Context, msg Message) error
	// Cancel interrupts the running turn, if any. The session stays usable.
	Cancel()
	// Events streams unified protocol events until the session ends.
	Events() <-chan protocol.Event
	Close() error
}
