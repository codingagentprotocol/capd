package codex

import (
	"encoding/json"
	"strings"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/proc"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// buildSpec assembles one `codex exec --json` turn. Conversation continuity
// uses `codex exec resume <thread_id>` with the id captured from
// thread.started.
func buildSpec(opts adapter.SessionOpts, nativeID, prompt string) proc.Spec {
	args := []string{"exec", "--json", "--skip-git-repo-check"}
	if opts.Model != "" {
		args = append(args, "-m", opts.Model)
	}
	switch opts.PermissionMode {
	case protocol.PermissionAcceptEdits, protocol.PermissionFull:
		// workspace-write sandbox with automatic execution; codex exec has
		// no finer-grained interactive mode.
		args = append(args, "--full-auto")
	}
	if nativeID != "" {
		args = append(args, "resume", nativeID)
	}
	args = append(args, prompt)
	return proc.Spec{Bin: "codex", Args: args, Cwd: opts.Cwd}
}

// codexEvent is the JSONL shape of `codex exec --json` (verified against
// codex-cli 0.128.0): thread.started / turn.started / item.started /
// item.updated / item.completed / turn.completed / turn.failed / error.
type codexEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id"`
	Message  string          `json:"message"` // on type=error
	Error    *codexError     `json:"error"`   // on turn.failed
	Usage    map[string]any  `json:"usage"`   // on turn.completed
	Item     json.RawMessage `json:"item"`
}

type codexError struct {
	Message string `json:"message"`
}

type codexItem struct {
	ID               string `json:"id"`
	Type             string `json:"item_type"`
	TypeAlt          string `json:"type"` // newer builds use "type"
	Text             string `json:"text"`
	Command          string `json:"command"`
	AggregatedOutput string `json:"aggregated_output"`
	ExitCode         *int   `json:"exit_code"`
	Status           string `json:"status"`
}

func (it *codexItem) kind() string {
	if it.Type != "" {
		return it.Type
	}
	return it.TypeAlt
}

// translate maps one codex stdout line to CAP events. Codex leaks tracing
// lines into stdout, so anything that is not a JSON object is skipped.
func translate(line string, emit adapter.EmitFunc) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return ""
	}
	var ev codexEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return ""
	}

	switch ev.Type {
	case "thread.started":
		// nativeSessionId is the cross-adapter key the session store watches.
		emit(protocol.EventSessionStarted, map[string]any{"nativeSessionId": ev.ThreadID, "threadId": ev.ThreadID})
		return ev.ThreadID

	case "turn.started":
		// no client-visible signal needed

	case "item.started", "item.updated", "item.completed":
		translateItem(ev, emit)

	case "turn.completed":
		emit(protocol.EventTaskDone, map[string]any{"ok": true, "usage": ev.Usage})

	case "turn.failed":
		msg := "turn failed"
		if ev.Error != nil {
			msg = ev.Error.Message
		}
		emit(protocol.EventError, map[string]any{"message": msg})
		emit(protocol.EventTaskDone, map[string]any{"ok": false})

	case "error":
		emit(protocol.EventError, map[string]any{"message": ev.Message})
	}
	return ""
}

func translateItem(ev codexEvent, emit adapter.EmitFunc) {
	var it codexItem
	if err := json.Unmarshal(ev.Item, &it); err != nil {
		return
	}
	// Raw item rides along so clients can render agent-specific detail.
	var raw map[string]any
	json.Unmarshal(ev.Item, &raw)

	switch it.kind() {
	case "agent_message":
		if ev.Type == "item.completed" {
			emit(protocol.EventOutputText, map[string]any{"text": it.Text})
		}
	case "reasoning":
		if ev.Type == "item.completed" {
			emit(protocol.EventOutputReasoning, map[string]any{"text": it.Text})
		}
	case "command_execution":
		switch ev.Type {
		case "item.started":
			emit(protocol.EventToolUse, map[string]any{"kind": "shell", "command": it.Command, "item": raw})
		case "item.completed":
			data := map[string]any{"kind": "shell", "command": it.Command, "output": it.AggregatedOutput, "item": raw}
			if it.ExitCode != nil {
				data["exitCode"] = *it.ExitCode
			}
			emit(protocol.EventToolResult, data)
		}
	default:
		// file_change, mcp_tool_call, web_search, todo_list, ...
		switch ev.Type {
		case "item.started":
			emit(protocol.EventToolUse, map[string]any{"kind": it.kind(), "item": raw})
		case "item.completed":
			emit(protocol.EventToolResult, map[string]any{"kind": it.kind(), "item": raw})
		}
	}
}
