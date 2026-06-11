package codex

import (
	"os"
	"testing"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

type captured struct {
	typ  protocol.EventType
	data map[string]any
}

func run(t *testing.T, lines []string) (events []captured, nativeID string) {
	t.Helper()
	emit := func(typ protocol.EventType, data map[string]any) {
		events = append(events, captured{typ, data})
	}
	for _, l := range lines {
		if id := translate(l, adapter.EmitFunc(emit)); id != "" {
			nativeID = id
		}
	}
	return events, nativeID
}

// Lines captured from a real codex-cli 0.128.0 run.
func TestTranslateRealStream(t *testing.T) {
	lines := []string{
		`{"type":"thread.started","thread_id":"019eb336-7a80-7ac1-bdd9-eb6cc95eab23"}`,
		`{"type":"turn.started"}`,
		`2026-06-10T20:25:54.375890Z ERROR rmcp::transport::worker: worker quit`, // log noise on stdout
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"hi"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}`,
	}
	events, nativeID := run(t, lines)

	if nativeID != "019eb336-7a80-7ac1-bdd9-eb6cc95eab23" {
		t.Fatalf("nativeID = %q", nativeID)
	}
	want := []protocol.EventType{protocol.EventSessionStarted, protocol.EventOutputText, protocol.EventTaskDone}
	if len(events) != len(want) {
		t.Fatalf("got %d events: %+v", len(events), events)
	}
	for i, w := range want {
		if events[i].typ != w {
			t.Fatalf("event %d = %s, want %s", i, events[i].typ, w)
		}
	}
	if events[1].data["text"] != "hi" {
		t.Fatalf("text = %v", events[1].data["text"])
	}
	if events[2].data["ok"] != true {
		t.Fatalf("done.ok = %v", events[2].data["ok"])
	}
}

func TestTranslateTurnFailed(t *testing.T) {
	lines := []string{
		`{"type":"error","message":"boom"}`,
		`{"type":"turn.failed","error":{"message":"boom"}}`,
	}
	events, _ := run(t, lines)
	want := []protocol.EventType{protocol.EventError, protocol.EventError, protocol.EventTaskDone}
	if len(events) != len(want) {
		t.Fatalf("got %+v", events)
	}
	if events[2].data["ok"] != false {
		t.Fatalf("done.ok = %v", events[2].data["ok"])
	}
}

func TestTranslateCommandExecution(t *testing.T) {
	lines := []string{
		`{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"ls","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"ls","aggregated_output":"a.txt","exit_code":0,"status":"completed"}}`,
	}
	events, _ := run(t, lines)
	if len(events) != 2 || events[0].typ != protocol.EventToolUse || events[1].typ != protocol.EventToolResult {
		t.Fatalf("got %+v", events)
	}
	if events[1].data["output"] != "a.txt" || events[1].data["exitCode"] != 0 {
		t.Fatalf("result data = %+v", events[1].data)
	}
}

func TestBuildSpecResolvesCodexBinary(t *testing.T) {
	spec := buildSpec(adapter.SessionOpts{Cwd: "/tmp"}, "", adapter.Message{Prompt: "hi"})
	if spec.Bin == "" {
		t.Fatal("empty binary path")
	}
	if spec.Bin != "codex" {
		if info, err := os.Stat(spec.Bin); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			t.Fatalf("resolved binary %q is not executable", spec.Bin)
		}
	}
}
