package adapter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/codingagentprotocol/capd/internal/proc"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// echoConfig drives /bin/sh as a stand-in agent: each turn echoes JSON
// events; the translator maps {"say":...} to output.text and captures
// {"id":...} as the native session id.
var echoConfig = TurnConfig{
	BuildSpec: func(opts SessionOpts, nativeID string, msg Message) proc.Spec {
		script := `echo '{"id":"native-1"}'; echo '{"say":"` + msg.Prompt + `"}'`
		return proc.Spec{Bin: "/bin/sh", Args: []string{"-c", script}}
	},
	Translate: func(line string, emit EmitFunc) string {
		var ev struct {
			ID  string `json:"id"`
			Say string `json:"say"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			return ""
		}
		if ev.Say != "" {
			emit(protocol.EventOutputText, map[string]any{"text": ev.Say})
		}
		return ev.ID
	},
}

func collectUntilDone(t *testing.T, s Session) []protocol.Event {
	t.Helper()
	var events []protocol.Event
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			events = append(events, ev)
			if ev.Type == protocol.EventTaskDone {
				return events
			}
		case <-deadline:
			t.Fatalf("no task.done; got %+v", events)
		}
	}
}

func TestTurnSessionBasicTurn(t *testing.T) {
	s := NewTurnSession(echoConfig, SessionOpts{})
	if err := s.Send(context.Background(), Message{Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	events := collectUntilDone(t, s)
	if len(events) != 2 || events[0].Type != protocol.EventOutputText || events[0].Data["text"] != "hi" {
		t.Fatalf("events = %+v", events)
	}
	if events[1].Data["ok"] != true {
		t.Fatalf("done = %+v", events[1])
	}
	s.Close()
}

func TestTurnSessionNativeIDFlowsToResume(t *testing.T) {
	var sawNativeID string
	cfg := echoConfig
	cfg.BuildSpec = func(opts SessionOpts, nativeID string, msg Message) proc.Spec {
		sawNativeID = nativeID
		return echoConfig.BuildSpec(opts, nativeID, msg)
	}
	s := NewTurnSession(cfg, SessionOpts{})
	s.Send(context.Background(), Message{Prompt: "a"})
	collectUntilDone(t, s)
	s.Send(context.Background(), Message{Prompt: "b"})
	collectUntilDone(t, s)
	if sawNativeID != "native-1" {
		t.Fatalf("turn 2 nativeID = %q, want native-1 from turn 1", sawNativeID)
	}
	s.Close()
}

func TestTurnSessionBusy(t *testing.T) {
	cfg := echoConfig
	cfg.BuildSpec = func(SessionOpts, string, Message) proc.Spec {
		return proc.Spec{Bin: "/bin/sleep", Args: []string{"30"}}
	}
	s := NewTurnSession(cfg, SessionOpts{})
	if err := s.Send(context.Background(), Message{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), Message{Prompt: "y"}); err != ErrTurnInProgress {
		t.Fatalf("want ErrTurnInProgress, got %v", err)
	}
	s.Close() // also exercises close-during-turn
}

func TestTurnSessionCancelReleasesSlot(t *testing.T) {
	cfg := echoConfig
	cfg.BuildSpec = func(opts SessionOpts, nativeID string, msg Message) proc.Spec {
		if msg.Prompt == "slow" {
			return proc.Spec{Bin: "/bin/sleep", Args: []string{"30"}}
		}
		return echoConfig.BuildSpec(opts, nativeID, msg)
	}
	s := NewTurnSession(cfg, SessionOpts{})
	s.Send(context.Background(), Message{Prompt: "slow"})
	s.Cancel()
	events := collectUntilDone(t, s)
	last := events[len(events)-1]
	if last.Data["canceled"] != true {
		t.Fatalf("done = %+v", last)
	}
	// Slot must be free immediately after task.done.
	if err := s.Send(context.Background(), Message{Prompt: "next"}); err != nil {
		t.Fatalf("send after cancel: %v", err)
	}
	collectUntilDone(t, s)
	s.Close()
}

func TestTurnSessionRejectsImagesWhenUnsupported(t *testing.T) {
	s := NewTurnSession(echoConfig, SessionOpts{})
	err := s.Send(context.Background(), Message{Prompt: "x", Images: []protocol.Attachment{{Type: "image", Path: "/p.png"}}})
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("want image rejection, got %v", err)
	}
	s.Close()
}

func TestTurnSessionSendAfterClose(t *testing.T) {
	s := NewTurnSession(echoConfig, SessionOpts{})
	s.Close()
	if err := s.Send(context.Background(), Message{Prompt: "x"}); err == nil {
		t.Fatal("want error after close")
	}
}

func TestRegistryOrderAndLookup(t *testing.T) {
	a := &PendingCLI{id: "b-agent"}
	b := &PendingCLI{id: "a-agent"}
	r := NewRegistry(a, b)
	all := r.All()
	if len(all) != 2 || all[0].ID() != "a-agent" || all[1].ID() != "b-agent" {
		t.Fatalf("order = %v, %v", all[0].ID(), all[1].ID())
	}
	if _, ok := r.Get("a-agent"); !ok {
		t.Fatal("lookup failed")
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("phantom agent")
	}
}

func TestPendingCLIRefusesSessions(t *testing.T) {
	p := NewPendingCLI("kimi", "Kimi", "definitely-not-installed-bin")
	info, err := p.Probe(context.Background())
	if err != nil || info.Available {
		t.Fatalf("probe = %+v, %v", info, err)
	}
	if _, err := p.StartSession(context.Background(), SessionOpts{}); err == nil {
		t.Fatal("pending adapter must refuse sessions")
	}
}

func TestRequireBin(t *testing.T) {
	if err := RequireBin("sh", "sh"); err != nil {
		t.Fatalf("sh should exist: %v", err)
	}
	if err := RequireBin("ghost", "no-such-binary-xyz"); err == nil {
		t.Fatal("want error for missing binary")
	}
}
