// Package opencode adapts the OpenCode CLI (https://opencode.ai).
//
// Headless invocation: opencode run <prompt> --format json
// Continuity: --session <sessionID>, captured from the event stream.
package opencode

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/proc"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

const ID = "opencode"

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) ID() string { return ID }

func (a *Adapter) Probe(ctx context.Context) (protocol.AgentInfo, error) {
	return adapter.ProbeCLI(ctx, ID, "OpenCode", "opencode", "--version")
}

func (a *Adapter) StartSession(_ context.Context, opts adapter.SessionOpts) (adapter.Session, error) {
	if err := adapter.RequireBin(ID, "opencode"); err != nil {
		return nil, err
	}
	if opts.Resume != "" {
		return adapter.NewTurnSessionResumed(turnConfig, opts, opts.Resume), nil
	}
	return adapter.NewTurnSession(turnConfig, opts), nil
}

var turnConfig = adapter.TurnConfig{BuildSpec: buildSpec, Translate: translate}

func buildSpec(opts adapter.SessionOpts, nativeID, prompt string) proc.Spec {
	args := []string{"run", prompt, "--format", "json"}
	if opts.Model != "" {
		args = append(args, "-m", opts.Model)
	}
	if nativeID != "" {
		args = append(args, "--session", nativeID)
	}
	return proc.Spec{Bin: "opencode", Args: args, Cwd: opts.Cwd}
}

// ocEvent is the JSONL shape of `opencode run --format json` (verified
// against opencode on this machine): step_start / text / tool / step_finish,
// each carrying sessionID and a part payload.
type ocEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      struct {
		Type   string         `json:"type"`
		Text   string         `json:"text"`
		Reason string         `json:"reason"`
		Cost   float64        `json:"cost"`
		Tokens map[string]any `json:"tokens"`
		Tool   string         `json:"tool"`
		State  map[string]any `json:"state"`
	} `json:"part"`
}

func translate(line string, emit adapter.EmitFunc) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return ""
	}
	var ev ocEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return ""
	}

	switch ev.Type {
	case "step_start":
		// session id rides on every event; report it once via the runner
	case "text":
		emit(protocol.EventOutputText, map[string]any{"text": ev.Part.Text})
	case "reasoning":
		emit(protocol.EventOutputReasoning, map[string]any{"text": ev.Part.Text})
	case "tool":
		status, _ := ev.Part.State["status"].(string)
		data := map[string]any{"kind": ev.Part.Tool, "item": ev.Part.State}
		if status == "completed" || status == "error" {
			emit(protocol.EventToolResult, data)
		} else {
			emit(protocol.EventToolUse, data)
		}
	case "step_finish":
		// One run can contain several steps (tool round trips); only the
		// final stop ends the turn.
		if ev.Part.Reason == "stop" {
			emit(protocol.EventTaskDone, map[string]any{
				"ok": true, "costUSD": ev.Part.Cost, "usage": ev.Part.Tokens,
			})
		}
	case "error":
		emit(protocol.EventError, map[string]any{"message": ev.Part.Text})
	}
	return ev.SessionID
}
