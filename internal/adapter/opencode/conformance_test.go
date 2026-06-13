package opencode

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

func TestOpenCodeAdapterConformanceStaticContract(t *testing.T) {
	adaptertest.CheckStaticContract(t, New(), adaptertest.StaticContract{
		ID:                 ID,
		RequiresCapability: true,
		RequiredCaps: protocol.AgentCapabilities{
			Model:  true,
			Images: true,
			Resume: true,
		},
		ForbiddenCaps: protocol.AgentCapabilities{
			Usage: true,
		},
	})
}

func TestOpenCodeAdapterConformanceLifecycleFixture(t *testing.T) {
	fake := writeFakeOpenCodeCLI(t)
	adaptertest.CheckSessionLifecycle(t, NewWithCLI("fake-opencode", "Fake OpenCode", fake), adaptertest.SessionContract{})
}

func TestOpenCodeAdapterConformanceResumeFixture(t *testing.T) {
	fake := writeFakeOpenCodeCLI(t)
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("CAPD_FAKE_OPENCODE_ARGS", argsPath)
	a := NewWithCLI("fake-opencode", "Fake OpenCode", fake)
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
	if !strings.Contains(string(raw), "--session") || !strings.Contains(string(raw), "fake-opencode-1") {
		t.Fatalf("fake cli args did not include session id: %s", raw)
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

func writeFakeOpenCodeCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "fake-opencode"
	script := `#!/bin/sh
if [ -n "$CAPD_FAKE_OPENCODE_ARGS" ]; then
  printf '%s\n' "$*" >> "$CAPD_FAKE_OPENCODE_ARGS"
fi
printf '%s\n' '{"type":"step_start","sessionID":"fake-opencode-1","part":{"type":"step-start"}}'
printf '%s\n' '{"type":"text","sessionID":"fake-opencode-1","part":{"type":"text","text":"fake hello"}}'
printf '%s\n' '{"type":"step_finish","sessionID":"fake-opencode-1","part":{"type":"finish","reason":"stop","cost":0.01,"tokens":{"input":1,"output":2}}}'
`
	if runtime.GOOS == "windows" {
		name += ".bat"
		script = `@echo off
if not "%CAPD_FAKE_OPENCODE_ARGS%"=="" echo %*>>"%CAPD_FAKE_OPENCODE_ARGS%"
echo {"type":"step_start","sessionID":"fake-opencode-1","part":{"type":"step-start"}}
echo {"type":"text","sessionID":"fake-opencode-1","part":{"type":"text","text":"fake hello"}}
echo {"type":"step_finish","sessionID":"fake-opencode-1","part":{"type":"finish","reason":"stop","cost":0.01,"tokens":{"input":1,"output":2}}}
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
