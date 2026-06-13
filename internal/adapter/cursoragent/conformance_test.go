package cursoragent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/adapter/adaptertest"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestCursorAgentAdapterConformanceStaticContract(t *testing.T) {
	adaptertest.CheckStaticContract(t, New(), adaptertest.StaticContract{
		ID:                 ID,
		RequiresCapability: true,
		RequiredCaps: protocol.AgentCapabilities{
			Resume: true,
		},
		ForbiddenCaps: protocol.AgentCapabilities{
			Images: true,
			Usage:  true,
		},
	})
}

func TestCursorAgentAdapterConformanceLifecycleFixture(t *testing.T) {
	fake := writeFakeCursorAgentCLI(t)
	adaptertest.CheckSessionLifecycle(t, NewWithCLI("fake-cursor-agent", "Fake Cursor CLI", fake), adaptertest.SessionContract{})
}

func TestCursorAgentAdapterConformanceResumeFixture(t *testing.T) {
	fake := writeFakeCursorAgentCLI(t)
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("CAPD_FAKE_CURSOR_AGENT_ARGS", argsPath)
	a := NewWithCLI("fake-cursor-agent", "Fake Cursor CLI", fake)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := a.StartSession(ctx, adapter.SessionOpts{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Send(ctx, adapter.Message{Prompt: "first"}); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, sess.Events(), protocol.EventTaskDone)
	if err := sess.Send(ctx, adapter.Message{Prompt: "second"}); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, sess.Events(), protocol.EventTaskDone)

	raw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "--resume") || !strings.Contains(string(raw), "fake-cursor-chat-1") {
		t.Fatalf("fake cli args did not include resume id: %s", raw)
	}
}

func waitForEvent(t *testing.T, events <-chan protocol.Event, typ protocol.EventType) protocol.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("events closed before %s", typ)
			}
			if ev.Type == typ {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", typ)
		}
	}
}

func writeFakeCursorAgentCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "fake-cursor-agent"
	script := `#!/bin/sh
if [ -n "$CAPD_FAKE_CURSOR_AGENT_ARGS" ]; then
  printf '%s\n' "$*" >> "$CAPD_FAKE_CURSOR_AGENT_ARGS"
fi
printf '%s\n' '{"type":"system","subtype":"init","chat_id":"fake-cursor-chat-1","session_id":"fake-cursor-session-1"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"fake hello"}]}}'
printf '%s\n' '{"type":"result","is_error":false,"result":"fake done","chat_id":"fake-cursor-chat-1","session_id":"fake-cursor-session-1"}'
`
	if runtime.GOOS == "windows" {
		name += ".bat"
		script = `@echo off
if not "%CAPD_FAKE_CURSOR_AGENT_ARGS%"=="" echo %*>>"%CAPD_FAKE_CURSOR_AGENT_ARGS%"
echo {"type":"system","subtype":"init","chat_id":"fake-cursor-chat-1","session_id":"fake-cursor-session-1"}
echo {"type":"assistant","message":{"content":[{"type":"text","text":"fake hello"}]}}
echo {"type":"result","is_error":false,"result":"fake done","chat_id":"fake-cursor-chat-1","session_id":"fake-cursor-session-1"}
`
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return path
}
