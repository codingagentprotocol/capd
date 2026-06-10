// Package protocol defines the wire format of the Coding Agent Protocol (CAP).
//
// CAP messages are framed as JSON-RPC 2.0 over a bidirectional transport
// (WebSocket for remote clients, stdio for out-of-process adapters).
package protocol

import "encoding/json"

const JSONRPCVersion = "2.0"

// Request is a JSON-RPC 2.0 request. A request without an ID is a notification.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification reports whether the request expects no response.
func (r *Request) IsNotification() bool { return r.ID == nil }

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Notification is a server-initiated message with no ID.
type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// NewResponse builds a success response for the given request ID.
func NewResponse(id *json.RawMessage, result any) (*Response, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &Response{JSONRPC: JSONRPCVersion, ID: id, Result: raw}, nil
}

// NewErrorResponse builds an error response for the given request ID.
func NewErrorResponse(id *json.RawMessage, err *Error) *Response {
	return &Response{JSONRPC: JSONRPCVersion, ID: id, Error: err}
}

// NewNotification builds a server-initiated notification.
func NewNotification(method string, params any) (*Notification, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return &Notification{JSONRPC: JSONRPCVersion, Method: method, Params: raw}, nil
}
