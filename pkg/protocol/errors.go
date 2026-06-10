package protocol

import "fmt"

// JSON-RPC 2.0 reserved error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// CAP-specific error codes live outside the reserved JSON-RPC range.
const (
	CodeAgentNotFound      = -32000 // requested agent is not installed or not discovered
	CodeAgentUnavailable   = -32001 // agent is installed but cannot be started
	CodeSessionNotFound    = -32002
	CodeSessionClosed      = -32003
	CodeUnauthorized       = -32004
	CodeVersionUnsupported = -32005 // protocol version negotiation failed
)

// Error is a JSON-RPC 2.0 error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("cap: %s (code %d)", e.Message, e.Code) }

func NewError(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}
