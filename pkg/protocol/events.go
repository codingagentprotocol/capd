package protocol

// EventType enumerates the unified event model. Every adapter translates its
// agent's native output stream into these events — clients never see dialects.
type EventType string

const (
	EventSessionStarted  EventType = "session.started"
	EventSessionEnded    EventType = "session.ended"
	EventOutputText      EventType = "output.text"      // assistant text
	EventOutputReasoning EventType = "output.reasoning" // thinking / reasoning text
	EventToolUse         EventType = "tool.use"         // agent invoked a tool
	EventToolResult      EventType = "tool.result"
	EventApprovalNeeded  EventType = "approval.needed" // agent waits for a human decision
	EventUsageUpdated    EventType = "usage.updated"   // account rate-limit snapshot pushed by the agent
	EventTaskDone        EventType = "task.done"       // turn finished, includes usage if known
	EventError           EventType = "error"
)

// Data conventions (soft contract, kept stable across adapters):
//   - output.text / output.reasoning: {"text": string, "delta": true?} —
//     delta marks an incremental chunk; the closing full text arrives in a
//     non-delta event with the same "itemId" when the agent provides one.
//   - approval.needed: {"approvalId": string, "kind": "command"|"fileChange"|...,
//     plus kind-specific detail (command, cwd, reason, changes)}. Answer with
//     the approval/reply method.
//   - usage.updated: agent-specific rate-limit snapshot (codex: rateLimits
//     with usedPercent / resetsAt / planType).

// Event is the payload of the "event" notification. Seq is assigned by the
// daemon, increases monotonically per session, and lets a re-attaching client
// resume the stream without gaps (session/attach with fromSeq).
type Event struct {
	SessionID string         `json:"sessionId"`
	Seq       uint64         `json:"seq"`
	Type      EventType      `json:"type"`
	Data      map[string]any `json:"data,omitempty"`
}
