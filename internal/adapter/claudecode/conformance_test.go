package claudecode

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

func TestClaudeCodeAdapterConformanceStaticContract(t *testing.T) {
	adaptertest.CheckStaticContract(t, New(), adaptertest.StaticContract{
		ID:                 ID,
		RequiresCapability: true,
		RequiredCaps: protocol.AgentCapabilities{
			Model:  true,
			Resume: true,
		},
		ForbiddenCaps: protocol.AgentCapabilities{
			Images: true,
			Usage:  true,
		},
	})
}

func TestClaudeCompatibleAdapterConformanceStaticContract(t *testing.T) {
	adaptertest.CheckStaticContract(t, NewWithCLI("codebuddy", "CodeBuddy", "codebuddy"), adaptertest.StaticContract{
		ID:                 "codebuddy",
		RequiresCapability: true,
		RequiredCaps: protocol.AgentCapabilities{
			Model:  true,
			Resume: true,
		},
	})
}

func TestClaudeCodeAdapterConformanceLifecycleFixture(t *testing.T) {
	fake := writeFakeClaudeCLI(t)
	adaptertest.CheckSessionLifecycle(t, NewWithCLI("fake-claude", "Fake Claude", fake), adaptertest.SessionContract{})
}

func TestClaudeCodeAdapterConformanceResumeFixture(t *testing.T) {
	fake := writeFakeClaudeCLI(t)
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("CAPD_FAKE_CLAUDE_ARGS", argsPath)
	a := NewWithCLI("fake-claude", "Fake Claude", fake)
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
	if !strings.Contains(string(raw), "--resume") || !strings.Contains(string(raw), "fake-claude-1") {
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

func writeFakeClaudeCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "fake-claude"
	script := `#!/bin/sh
if [ -n "$CAPD_FAKE_CLAUDE_ARGS" ]; then
  printf '%s\n' "$*" >> "$CAPD_FAKE_CLAUDE_ARGS"
fi
printf '%s\n' '{"type":"system","subtype":"init","session_id":"fake-claude-1","model":"fake-model","cwd":"/tmp"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"fake hello"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"fake done","session_id":"fake-claude-1"}'
`
	if runtime.GOOS == "windows" {
		name += ".bat"
		script = `@echo off
if not "%CAPD_FAKE_CLAUDE_ARGS%"=="" echo %*>>"%CAPD_FAKE_CLAUDE_ARGS%"
echo {"type":"system","subtype":"init","session_id":"fake-claude-1","model":"fake-model","cwd":"C:\\tmp"}
echo {"type":"assistant","message":{"content":[{"type":"text","text":"fake hello"}]}}
echo {"type":"result","subtype":"success","is_error":false,"result":"fake done","session_id":"fake-claude-1"}
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
