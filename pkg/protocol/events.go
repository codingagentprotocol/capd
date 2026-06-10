package protocol

// EventType enumerates the unified event model. Every adapter translates its
// agent's native output stream into these events — clients never see dialects.
type EventType string

const (
	EventSessionStarted EventType = "session.started"
	EventSessionEnded   EventType = "session.ended"
	EventOutputText     EventType = "output.text"      // assistant text delta
	EventToolUse        EventType = "tool.use"         // agent invoked a tool
	EventToolResult     EventType = "tool.result"
	EventApprovalNeeded EventType = "approval.needed"  // agent waits for a human decision
	EventTaskDone       EventType = "task.done"        // turn finished, includes usage if known
	EventError          EventType = "error"
)

// Event is the payload of the "event" notification.
type Event struct {
	SessionID string         `json:"sessionId"`
	Type      EventType      `json:"type"`
	Data      map[string]any `json:"data,omitempty"`
}
