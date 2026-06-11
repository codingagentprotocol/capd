package gemini

import (
	"testing"

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestTranslateClaudeCompatibleShape(t *testing.T) {
	lines := []string{
		`{"type":"init","session_id":"g1","model":"gemini-pro"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"result","result":"hi","is_error":false}`,
	}
	var types []protocol.EventType
	var nativeID string
	for _, l := range lines {
		if id := translate(l, func(typ protocol.EventType, data map[string]any) {
			types = append(types, typ)
		}); id != "" {
			nativeID = id
		}
	}
	if nativeID != "g1" {
		t.Fatalf("nativeID = %q", nativeID)
	}
	want := []protocol.EventType{protocol.EventSessionStarted, protocol.EventOutputText, protocol.EventTaskDone}
	for i, w := range want {
		if types[i] != w {
			t.Fatalf("types = %v", types)
		}
	}
}

// Non-JSON output (auth prompts, banners) must surface, not vanish.
func TestTranslatePlainTextFallback(t *testing.T) {
	var got string
	translate("Please set an Auth method", func(typ protocol.EventType, data map[string]any) {
		if typ == protocol.EventOutputText {
			got, _ = data["text"].(string)
		}
	})
	if got != "Please set an Auth method" {
		t.Fatalf("got %q", got)
	}
}
