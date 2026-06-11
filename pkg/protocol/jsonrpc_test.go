package protocol

import (
	"encoding/json"
	"testing"
)

func TestRequestNotificationDetection(t *testing.T) {
	id := json.RawMessage(`1`)
	if (&Request{ID: &id}).IsNotification() {
		t.Fatal("request with id is not a notification")
	}
	if !(&Request{}).IsNotification() {
		t.Fatal("request without id is a notification")
	}
}

func TestResponseShapes(t *testing.T) {
	id := json.RawMessage(`7`)
	resp, err := NewResponse(&id, map[string]any{"x": 1})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(resp)
	want := `{"jsonrpc":"2.0","id":7,"result":{"x":1}}`
	if string(data) != want {
		t.Fatalf("got %s", data)
	}

	errResp := NewErrorResponse(&id, NewError(CodeAgentNotFound, "no agent %q", "x"))
	data, _ = json.Marshal(errResp)
	var back map[string]any
	json.Unmarshal(data, &back)
	if back["error"].(map[string]any)["code"] != float64(CodeAgentNotFound) {
		t.Fatalf("got %s", data)
	}

	n, _ := NewNotification("event", Event{SessionID: "s", Seq: 3, Type: EventTaskDone})
	data, _ = json.Marshal(n)
	if string(data) != `{"jsonrpc":"2.0","method":"event","params":{"sessionId":"s","seq":3,"type":"task.done"}}` {
		t.Fatalf("got %s", data)
	}
}
