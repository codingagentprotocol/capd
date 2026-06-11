package opencode

import (
	"testing"

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

type captured struct {
	typ  protocol.EventType
	data map[string]any
}

// Lines captured from a real opencode 1.1.1 run.
func TestTranslateRealStream(t *testing.T) {
	lines := []string{
		`{"type":"step_start","timestamp":1781161894484,"sessionID":"ses_14a7","part":{"id":"prt_1","sessionID":"ses_14a7","messageID":"msg_1","type":"step-start"}}`,
		`{"type":"text","timestamp":1781161895807,"sessionID":"ses_14a7","part":{"id":"prt_2","sessionID":"ses_14a7","messageID":"msg_1","type":"text","text":"hi"}}`,
		`{"type":"step_finish","timestamp":1781161895807,"sessionID":"ses_14a7","part":{"id":"prt_3","type":"step-finish","reason":"stop","cost":0.001,"tokens":{"input":10877,"output":2}}}`,
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
	if nativeID != "ses_14a7" {
		t.Fatalf("nativeID = %q", nativeID)
	}
	if len(events) != 2 || events[0].typ != protocol.EventOutputText || events[0].data["text"] != "hi" {
		t.Fatalf("events = %+v", events)
	}
	if events[1].typ != protocol.EventTaskDone || events[1].data["ok"] != true || events[1].data["costUSD"] != 0.001 {
		t.Fatalf("done = %+v", events[1])
	}
}

// A tool round trip means several steps; only reason "stop" ends the turn.
func TestTranslateMultiStepEndsOnce(t *testing.T) {
	lines := []string{
		`{"type":"step_finish","sessionID":"s","part":{"type":"step-finish","reason":"tool-calls"}}`,
		`{"type":"tool","sessionID":"s","part":{"type":"tool","tool":"bash","state":{"status":"completed","output":"ok"}}}`,
		`{"type":"step_finish","sessionID":"s","part":{"type":"step-finish","reason":"stop"}}`,
	}
	var dones, tools int
	for _, l := range lines {
		translate(l, func(typ protocol.EventType, data map[string]any) {
			if typ == protocol.EventTaskDone {
				dones++
			}
			if typ == protocol.EventToolResult {
				tools++
			}
		})
	}
	if dones != 1 || tools != 1 {
		t.Fatalf("dones=%d tools=%d", dones, tools)
	}
}
