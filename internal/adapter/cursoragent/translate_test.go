package cursoragent

import (
	"testing"

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestTranslateDocumentedShape(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","chat_id":"c1"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"result","result":"hi","is_error":false,"chat_id":"c1"}`,
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
	if nativeID != "c1" {
		t.Fatalf("nativeID = %q", nativeID)
	}
	want := []protocol.EventType{protocol.EventSessionStarted, protocol.EventOutputText, protocol.EventTaskDone}
	for i, w := range want {
		if types[i] != w {
			t.Fatalf("types = %v", types)
		}
	}
}
