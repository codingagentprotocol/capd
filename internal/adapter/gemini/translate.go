package gemini

import (
	"encoding/json"
	"strings"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/proc"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// buildSpec assembles one `gemini -p` turn with stream-json output.
// Gemini CLI's --resume takes "latest" or a numeric index rather than a
// stable id, so cross-turn continuity is not wired up yet.
//
// TODO: verify the stream-json shape against a live gemini login; the
// translator below targets the documented Claude-compatible format and
// falls back to plain text for anything it does not recognize.
func buildSpec(opts adapter.SessionOpts, _ string, prompt string) proc.Spec {
	args := []string{"-p", prompt, "--output-format", "stream-json"}
	switch opts.PermissionMode {
	case protocol.PermissionAcceptEdits:
		args = append(args, "--approval-mode", "auto_edit")
	case protocol.PermissionFull:
		args = append(args, "--yolo")
	}
	return proc.Spec{Bin: "gemini", Args: args, Cwd: opts.Cwd}
}

type geminiEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	Message   *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	Result  string         `json:"result"`
	IsError bool           `json:"is_error"`
	Usage   map[string]any `json:"usage"`
}

func translate(line string, emit adapter.EmitFunc) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "{") {
		// Auth prompts and banners arrive as plain text; surface them.
		emit(protocol.EventOutputText, map[string]any{"text": line})
		return ""
	}
	var ev geminiEvent
	if err := json.Unmarshal([]byte(trimmed), &ev); err != nil {
		emit(protocol.EventOutputText, map[string]any{"text": line})
		return ""
	}

	switch ev.Type {
	case "system", "init":
		emit(protocol.EventSessionStarted, map[string]any{
			"nativeSessionId": ev.SessionID, "model": ev.Model,
		})
		return ev.SessionID
	case "assistant", "message":
		if ev.Message != nil {
			for _, b := range ev.Message.Content {
				if b.Type == "text" {
					emit(protocol.EventOutputText, map[string]any{"text": b.Text})
				}
			}
		}
	case "result":
		emit(protocol.EventTaskDone, map[string]any{
			"ok": !ev.IsError, "result": ev.Result, "usage": ev.Usage,
		})
	}
	return ""
}
