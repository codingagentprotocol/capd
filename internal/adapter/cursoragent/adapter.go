// Package cursoragent adapts Cursor's CLI agent (cursor-agent).
//
// Headless invocation: cursor-agent -p <prompt> --output-format stream-json
// Continuity: --resume <chatId>.
//
// TODO: verify the stream against a logged-in cursor-agent; the translator
// targets its documented Claude-compatible stream-json shape.
package cursoragent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/proc"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

const ID = "cursor-agent"

type Adapter struct {
	id, name, bin string
}

func New() *Adapter { return NewWithCLI(ID, "Cursor CLI", "cursor-agent") }

func NewWithCLI(id, name, bin string) *Adapter {
	return &Adapter{id: id, name: name, bin: bin}
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) Capabilities() protocol.AgentCapabilities {
	return protocol.AgentCapabilities{
		Resume: true,
	}
}

func (a *Adapter) Probe(ctx context.Context) (protocol.AgentInfo, error) {
	return adapter.ProbeCLI(ctx, a.id, a.name, a.bin, "--version")
}

func (a *Adapter) StartSession(_ context.Context, opts adapter.SessionOpts) (adapter.Session, error) {
	if err := adapter.RequireBin(a.id, a.bin); err != nil {
		return nil, err
	}
	config := adapter.TurnConfig{
		BuildSpec: func(opts adapter.SessionOpts, nativeID string, msg adapter.Message) proc.Spec {
			spec := buildSpec(opts, nativeID, msg)
			spec.Bin = a.bin
			return spec
		},
		Translate: translate,
	}
	if opts.Resume != "" {
		return adapter.NewTurnSessionResumed(config, opts, opts.Resume), nil
	}
	return adapter.NewTurnSession(config, opts), nil
}

func buildSpec(opts adapter.SessionOpts, nativeID string, msg adapter.Message) proc.Spec {
	prompt := msg.Prompt
	args := []string{"-p", prompt, "--output-format", "stream-json"}
	if opts.PermissionMode == protocol.PermissionFull {
		args = append(args, "--force")
	}
	if nativeID != "" {
		args = append(args, "--resume", nativeID)
	}
	return proc.Spec{Bin: "cursor-agent", Args: args, Cwd: opts.Cwd}
}

type cursorEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	ChatID    string `json:"chat_id"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	Message   *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

func (e *cursorEvent) nativeID() string {
	if e.ChatID != "" {
		return e.ChatID
	}
	return e.SessionID
}

func translate(line string, emit adapter.EmitFunc) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return ""
	}
	var ev cursorEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return ""
	}

	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			emit(protocol.EventSessionStarted, map[string]any{"nativeSessionId": ev.nativeID()})
			return ev.nativeID()
		}
	case "assistant":
		if ev.Message != nil {
			for _, b := range ev.Message.Content {
				if b.Type == "text" {
					emit(protocol.EventOutputText, map[string]any{"text": b.Text})
				}
			}
		}
	case "result":
		emit(protocol.EventTaskDone, map[string]any{"ok": !ev.IsError, "result": ev.Result})
		return ev.nativeID()
	}
	return ""
}
