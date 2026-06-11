package codex

import (
	"encoding/json"
	"testing"

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func newTestAppSession() *appSession {
	profile := &appServerProfile{sessions: map[string]*appSession{}}
	return &appSession{
		owner:    profile,
		threadID: "t1",
		events:   make(chan protocol.Event, 64),
	}
}

func drain(s *appSession) []protocol.Event {
	var out []protocol.Event
	for {
		select {
		case ev := <-s.events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func notify(s *appSession, method, params string) {
	s.handleNotification(method, json.RawMessage(params))
}

func TestAppServerDeltaTranslation(t *testing.T) {
	s := newTestAppSession()
	notify(s, "item/agentMessage/delta", `{"threadId":"t1","itemId":"m1","delta":"he"}`)
	notify(s, "item/agentMessage/delta", `{"threadId":"t1","itemId":"m1","delta":"llo"}`)
	notify(s, "item/completed", `{"item":{"type":"agentMessage","id":"m1","text":"hello"}}`)

	evs := drain(s)
	if len(evs) != 3 {
		t.Fatalf("got %+v", evs)
	}
	if evs[0].Data["delta"] != true || evs[0].Data["text"] != "he" {
		t.Fatalf("delta = %+v", evs[0])
	}
	if evs[2].Data["final"] != true || evs[2].Data["text"] != "hello" {
		t.Fatalf("final = %+v", evs[2])
	}
}

func TestAppServerTurnCompletedCarriesResultAndUsage(t *testing.T) {
	s := newTestAppSession()
	notify(s, "item/completed", `{"item":{"type":"agentMessage","id":"m1","text":"408"}}`)
	notify(s, "thread/tokenUsage/updated", `{"threadId":"t1","tokenUsage":{"total":{"totalTokens":42}}}`)
	notify(s, "turn/completed", `{"threadId":"t1","turn":{"status":"completed"}}`)

	evs := drain(s)
	done := evs[len(evs)-1]
	if done.Type != protocol.EventTaskDone || done.Data["ok"] != true {
		t.Fatalf("done = %+v", done)
	}
	if done.Data["result"] != "408" {
		t.Fatalf("result = %v", done.Data["result"])
	}
	usage, _ := done.Data["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("usage missing: %+v", done.Data)
	}
}

func TestAppServerTurnFailed(t *testing.T) {
	s := newTestAppSession()
	notify(s, "turn/failed", `{"threadId":"t1","turn":{"status":"failed","error":{"message":"boom"}}}`)
	evs := drain(s)
	done := evs[len(evs)-1]
	if done.Type != protocol.EventTaskDone || done.Data["ok"] != false {
		t.Fatalf("done = %+v", done)
	}
}

func TestAppServerCommandExecutionEvents(t *testing.T) {
	s := newTestAppSession()
	notify(s, "item/started", `{"item":{"type":"commandExecution","id":"c1","command":"ls"}}`)
	notify(s, "item/commandExecution/outputDelta", `{"threadId":"t1","itemId":"c1","chunk":"a.txt"}`)
	notify(s, "item/completed", `{"item":{"type":"commandExecution","id":"c1","command":"ls","aggregatedOutput":"a.txt\n","exitCode":0}}`)

	evs := drain(s)
	if evs[0].Type != protocol.EventToolUse || evs[0].Data["command"] != "ls" {
		t.Fatalf("use = %+v", evs[0])
	}
	if evs[1].Type != protocol.EventToolResult || evs[1].Data["delta"] != true {
		t.Fatalf("delta out = %+v", evs[1])
	}
	if evs[2].Type != protocol.EventToolResult || evs[2].Data["output"] != "a.txt\n" {
		t.Fatalf("result = %+v", evs[2])
	}
}

func TestAppServerApprovalRequestToEvent(t *testing.T) {
	s := newTestAppSession()
	s.handleServerRequest(json.RawMessage(`7`), "item/commandExecution/requestApproval",
		json.RawMessage(`{"threadId":"t1","turnId":"u1","itemId":"c1","command":"rm -rf /","cwd":"/tmp","reason":"dangerous"}`))

	evs := drain(s)
	if len(evs) != 1 || evs[0].Type != protocol.EventApprovalNeeded {
		t.Fatalf("got %+v", evs)
	}
	d := evs[0].Data
	if d["kind"] != "command" || d["command"] != "rm -rf /" || d["approvalId"] == "" {
		t.Fatalf("data = %+v", d)
	}
	if _, has := d["threadId"]; has {
		t.Fatal("threadId should be stripped")
	}
	s.mu.Lock()
	_, ok := s.approvals[d["approvalId"].(string)]
	s.mu.Unlock()
	if !ok {
		t.Fatal("approval not registered")
	}
}

func TestAppServerRateLimitPush(t *testing.T) {
	s := newTestAppSession()
	notify(s, "account/rateLimits/updated", `{"rateLimits":{"planType":"pro"}}`)
	evs := drain(s)
	if len(evs) != 1 || evs[0].Type != protocol.EventUsageUpdated {
		t.Fatalf("got %+v", evs)
	}
}

func TestDecisionTranslation(t *testing.T) {
	cases := map[string]string{
		protocol.DecisionApprove:       "accept",
		protocol.DecisionApproveAlways: "acceptForSession",
		protocol.DecisionDeny:          "reject",
		"garbage":                      "reject",
	}
	for in, want := range cases {
		if got := translateDecision(in); got != want {
			t.Fatalf("translateDecision(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPermissionMapping(t *testing.T) {
	cases := []struct{ mode, approval, sandbox string }{
		{protocol.PermissionDefault, "on-request", "read-only"},
		{protocol.PermissionAcceptEdits, "on-request", "workspace-write"},
		{protocol.PermissionFull, "never", "danger-full-access"},
		{"garbage", "on-request", "read-only"},
	}
	for _, c := range cases {
		a, sb := permissionMapping(c.mode)
		if a != c.approval || sb != c.sandbox {
			t.Fatalf("mode %q -> %s/%s, want %s/%s", c.mode, a, sb, c.approval, c.sandbox)
		}
	}
}

func TestEngineDeathClosesSessions(t *testing.T) {
	profile := &appServerProfile{sessions: map[string]*appSession{}}
	s := &appSession{owner: profile, threadID: "t1", events: make(chan protocol.Event, 8)}
	profile.sessions["t1"] = s

	profile.handleEngineDeath()

	var sawError, sawDone bool
	for ev := range s.events {
		if ev.Type == protocol.EventError {
			sawError = true
		}
		if ev.Type == protocol.EventTaskDone && ev.Data["engineDied"] == true {
			sawDone = true
		}
	}
	if !sawError || !sawDone {
		t.Fatalf("error=%v done=%v", sawError, sawDone)
	}
	if len(profile.sessions) != 0 {
		t.Fatal("sessions not cleared")
	}
}

func TestAppServerProfileUsesDefaultKeyForEmptyEnv(t *testing.T) {
	if got := profileKey(nil); got != "default" {
		t.Fatalf("profileKey(nil) = %q", got)
	}
}

func TestAppServerProfileSeparatesEnvironments(t *testing.T) {
	var pool appServer
	first := pool.profile([]string{"CODEX_HOME=/tmp/capd/a"})
	again := pool.profile([]string{"CODEX_HOME=/tmp/capd/a"})
	second := pool.profile([]string{"CODEX_HOME=/tmp/capd/b"})

	if first != again {
		t.Fatal("same environment did not reuse profile")
	}
	if first == second {
		t.Fatal("different environments reused one profile")
	}
	if first.env[0] != "CODEX_HOME=/tmp/capd/a" {
		t.Fatalf("profile env = %#v", first.env)
	}
}
