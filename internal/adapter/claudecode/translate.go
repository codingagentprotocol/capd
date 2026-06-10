package claudecode

import (
	"encoding/json"
	"strings"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/proc"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// buildSpec assembles one `claude -p` turn. Continuity uses --resume with the
// session_id captured from the init event.
func buildSpec(opts adapter.SessionOpts, nativeID, prompt string) proc.Spec {
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose"}
	switch opts.PermissionMode {
	case protocol.PermissionAcceptEdits:
		args = append(args, "--permission-mode", "acceptEdits")
	case protocol.PermissionFull:
		args = append(args, "--permission-mode", "bypassPermissions")
	}
	if nativeID != "" {
		args = append(args, "--resume", nativeID)
	}
	return proc.Spec{Bin: "claude", Args: args, Cwd: opts.Cwd}
}

// claudeEvent is the stream-json shape of `claude -p` (verified against
// Claude Code 1.0.41): system/init, assistant, user, result.
type claudeEvent struct {
	Type      string  `json:"type"`
	Subtype   string  `json:"subtype"`
	SessionID string  `json:"session_id"`
	Model     string  `json:"model"`
	Cwd       string  `json:"cwd"`
	Message   *claudeMessage `json:"message"`
	// result event fields
	Result       string         `json:"result"`
	IsError      bool           `json:"is_error"`
	NumTurns     int            `json:"num_turns"`
	TotalCostUSD float64        `json:"total_cost_usd"`
	Usage        map[string]any `json:"usage"`
}

type claudeMessage struct {
	Content []claudeBlock `json:"content"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`           // tool_use id
	Name      string          `json:"name"`         // tool name
	Input     json.RawMessage `json:"input"`        // tool input
	ToolUseID string          `json:"tool_use_id"`  // on tool_result
	Content   json.RawMessage `json:"content"`      // tool_result payload
}

func translate(line string, emit adapter.EmitFunc) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return ""
	}
	var ev claudeEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return ""
	}

	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			emit(protocol.EventSessionStarted, map[string]any{
				"nativeSessionId": ev.SessionID, "model": ev.Model, "cwd": ev.Cwd,
			})
			return ev.SessionID
		}

	case "assistant":
		if ev.Message != nil {
			translateBlocks(ev.Message.Content, emit)
		}

	case "user":
		// Tool results come back wrapped in user messages.
		if ev.Message != nil {
			for _, b := range ev.Message.Content {
				if b.Type == "tool_result" {
					emit(protocol.EventToolResult, map[string]any{
						"toolUseId": b.ToolUseID, "content": rawToAny(b.Content),
					})
				}
			}
		}

	case "result":
		emit(protocol.EventTaskDone, map[string]any{
			"ok": !ev.IsError, "result": ev.Result, "numTurns": ev.NumTurns,
			"costUSD": ev.TotalCostUSD, "usage": ev.Usage,
		})
		return ev.SessionID
	}
	return ""
}

func translateBlocks(blocks []claudeBlock, emit adapter.EmitFunc) {
	for _, b := range blocks {
		switch b.Type {
		case "text":
			emit(protocol.EventOutputText, map[string]any{"text": b.Text})
		case "thinking":
			emit(protocol.EventOutputReasoning, map[string]any{"text": b.Thinking})
		case "tool_use":
			emit(protocol.EventToolUse, map[string]any{
				"kind": b.Name, "toolUseId": b.ID, "input": rawToAny(b.Input),
			})
		}
	}
}

func rawToAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}
