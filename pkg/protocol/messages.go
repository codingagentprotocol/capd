package protocol

// Client → daemon methods.
const (
	MethodInitialize    = "initialize"     // version negotiation, must be the first call
	MethodAgentsList    = "agents/list"    // list discovered agent CLIs
	MethodSessionCreate = "session/create" // start an agent session
	MethodSessionAttach = "session/attach" // re-attach to a live or persisted session
	MethodSessionClose  = "session/close"
	MethodTaskSend      = "task/send"      // send a prompt/task into a session
	MethodTaskCancel    = "task/cancel"    // interrupt the running task
	MethodApprovalReply = "approval/reply" // answer a pending tool-use approval
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

type SessionCreateParams struct {
	AgentID string `json:"agentId"`
	Cwd     string `json:"cwd,omitempty"`     // working directory for the agent
	Resume  string `json:"resume,omitempty"`  // agent-native session id to resume
}

type SessionCreateResult struct {
	SessionID string `json:"sessionId"`
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

// OKResult is the generic acknowledgement for methods with no richer result.
type OKResult struct {
	OK bool `json:"ok"`
}
