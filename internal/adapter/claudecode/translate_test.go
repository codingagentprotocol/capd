package claudecode

import (
	"testing"

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

type captured struct {
	typ  protocol.EventType
	data map[string]any
}

// Lines captured from a real Claude Code 1.0.41 run (content abridged).
func TestTranslateRealStream(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","cwd":"/private/tmp","session_id":"08ac48e4-6835-4967-b91c-f553ad6dbd92","tools":["Bash","Read"],"model":"claude-sonnet-4-6","permissionMode":"default"}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","type":"message","content":[{"type":"text","text":"hi"}]},"session_id":"08ac48e4-6835-4967-b91c-f553ad6dbd92"}`,
		`{"type":"result","subtype":"success","is_error":false,"duration_ms":7450,"num_turns":1,"result":"hi","session_id":"08ac48e4-6835-4967-b91c-f553ad6dbd92","total_cost_usd":0.01,"usage":{"input_tokens":5,"output_tokens":2}}`,
	}

	var events []captured
	var nativeID string
	for _, l := range lines {
		if id := translate(l, func(typ protocol.EventType, data map[string]any) {
			events = append(events, captured{typ, data})
		}); id != "" {
			nativeID = id
		}
	}

	if nativeID != "08ac48e4-6835-4967-b91c-f553ad6dbd92" {
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
	if events[2].data["ok"] != true || events[2].data["costUSD"] != 0.01 {
		t.Fatalf("done data = %+v", events[2].data)
	}
}

func TestTranslateToolUse(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","content":"a.txt"}]}}`,
	}
	var events []captured
	for _, l := range lines {
		translate(l, func(typ protocol.EventType, data map[string]any) {
			events = append(events, captured{typ, data})
		})
	}
	if len(events) != 2 || events[0].typ != protocol.EventToolUse || events[1].typ != protocol.EventToolResult {
		t.Fatalf("got %+v", events)
	}
	if events[0].data["kind"] != "Bash" || events[1].data["toolUseId"] != "tu1" {
		t.Fatalf("data = %+v %+v", events[0].data, events[1].data)
	}
}
