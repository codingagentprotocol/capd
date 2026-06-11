package protocol

// Client → daemon methods.
const (
	MethodInitialize    = "initialize"     // version negotiation, must be the first call
	MethodAgentsList    = "agents/list"    // list discovered agent CLIs
	MethodAgentsUsage   = "agents/usage"   // account usage / rate-limit data for one agent
	MethodSessionCreate = "session/create" // start an agent session
	MethodSessionList   = "session/list"   // enumerate sessions and their liveness
	MethodSessionAttach = "session/attach" // re-attach to a live or persisted session
	MethodSessionClose  = "session/close"
	MethodTaskSend      = "task/send"      // send a prompt/task into a session
	MethodTaskSteer     = "task/steer"     // inject guidance into the RUNNING turn
	MethodTaskCancel    = "task/cancel"    // interrupt the running task
	MethodApprovalReply = "approval/reply" // answer a pending tool-use approval
)

// Approval decisions, translated by each adapter to its agent's vocabulary.
const (
	DecisionApprove       = "approve"
	DecisionApproveAlways = "approveAlways" // approve and stop asking for this kind in this session
	DecisionDeny          = "deny"
)

// Daemon → client notifications.
const (
	MethodEvent = "event" // streamed session events, see events.go
)

type InitializeParams struct {
	ProtocolVersion string     `json:"protocolVersion"`
	Client          ClientInfo `json:"client"`
}

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type InitializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	Daemon          struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"daemon"`
}

// AgentInfo describes one coding agent CLI discovered on this machine.
type AgentInfo struct {
	ID        string `json:"id"`   // stable identifier, e.g. "claude-code"
	Name      string `json:"name"` // human-readable, e.g. "Claude Code"
	Bin       string `json:"bin,omitempty"`     // resolved binary path
	Version   string `json:"version,omitempty"` // reported by the CLI itself
	Available bool   `json:"available"`
}

type AgentsListResult struct {
	Agents []AgentInfo `json:"agents"`
}

type AgentsUsageParams struct {
	AgentID string `json:"agentId"`
}

// AgentsUsageResult carries the agent's account usage snapshot. The shape is
// agent-specific (codex: rateLimits with usedPercent / resetsAt / planType);
// capd passes it through rather than flattening dialects prematurely.
type AgentsUsageResult struct {
	AgentID string         `json:"agentId"`
	Usage   map[string]any `json:"usage"`
}

// Permission modes, mapped by each adapter onto the agent's native flags.
const (
	PermissionDefault     = ""            // agent's own default (usually safest)
	PermissionAcceptEdits = "acceptEdits" // auto-approve file edits, ask for the rest
	PermissionFull        = "full"        // auto-approve everything the agent allows
)

type SessionCreateParams struct {
	AgentID string `json:"agentId"`
	Cwd     string `json:"cwd,omitempty"`    // working directory for the agent
	Resume  string `json:"resume,omitempty"` // agent-native session id to resume
	// PermissionMode is one of the Permission* constants. Interactive
	// per-action approval (approval.needed events) is a future milestone.
	PermissionMode string `json:"permissionMode,omitempty"`
	// Model is the agent-native model identifier (e.g. "gpt-5.3-codex",
	// "claude-sonnet-4-6"). Empty uses the agent's default.
	Model string `json:"model,omitempty"`
	// Effort is the agent-native reasoning effort, where supported
	// (codex: minimal/low/medium/high/xhigh).
	Effort string `json:"effort,omitempty"`
}

type SessionCreateResult struct {
	SessionID string `json:"sessionId"`
}

// Session states reported by session/list.
const (
	SessionStateLive   = "live"   // running in this daemon right now
	SessionStateStored = "stored" // persisted; revives automatically on attach/send
	SessionStateEnded  = "ended"  // closed for good
)

type SessionInfo struct {
	SessionID string `json:"sessionId"`
	AgentID   string `json:"agentId"`
	Cwd       string `json:"cwd,omitempty"`
	State     string `json:"state"` // one of the SessionState* constants
	CreatedAt int64  `json:"createdAt,omitempty"` // unix seconds
}

type SessionListResult struct {
	Sessions []SessionInfo `json:"sessions"`
}

type SessionAttachParams struct {
	SessionID string `json:"sessionId"`
	FromSeq   uint64 `json:"fromSeq"` // replay buffered events from this seq onward
}

type SessionAttachResult struct {
	SessionID string `json:"sessionId"`
	NextSeq   uint64 `json:"nextSeq"` // seq the live stream will continue from
}

type SessionCloseParams struct {
	SessionID string `json:"sessionId"`
}

type TaskSendParams struct {
	SessionID string `json:"sessionId"`
	Prompt    string `json:"prompt"`
}

type TaskCancelParams struct {
	SessionID string `json:"sessionId"`
}

// TaskSteerParams adds guidance to the turn that is currently running,
// without interrupting it. Errors if the agent does not support steering.
type TaskSteerParams struct {
	SessionID string `json:"sessionId"`
	Prompt    string `json:"prompt"`
}

// ApprovalReplyParams answers a pending approval.needed event.
type ApprovalReplyParams struct {
	SessionID  string `json:"sessionId"`
	ApprovalID string `json:"approvalId"` // from the approval.needed event data
	Decision   string `json:"decision"`   // one of the Decision* constants
}

// OKResult is the generic acknowledgement for methods with no richer result.
type OKResult struct {
	OK bool `json:"ok"`
}
