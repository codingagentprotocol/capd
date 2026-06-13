package gemini

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/codingagentprotocol/capd/internal/adapter/adaptertest"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestGeminiAdapterConformanceStaticContract(t *testing.T) {
	adaptertest.CheckStaticContract(t, New(), adaptertest.StaticContract{
		ID:                 ID,
		RequiresCapability: true,
		RequiredCaps: protocol.AgentCapabilities{
			Model: true,
		},
		ForbiddenCaps: protocol.AgentCapabilities{
			Images: true,
			Resume: true,
			Usage:  true,
		},
	})
}

func TestGeminiCompatibleAdapterConformanceStaticContract(t *testing.T) {
	for _, tc := range []struct {
		id, name, bin string
	}{
		{"qwen-code", "Qwen Code", "qwen"},
		{"iflow", "iFlow", "iflow"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			adaptertest.CheckStaticContract(t, NewWithCLI(tc.id, tc.name, tc.bin), adaptertest.StaticContract{
				ID:                 tc.id,
				RequiresCapability: true,
				RequiredCaps: protocol.AgentCapabilities{
					Model: true,
				},
			})
		})
	}
}

func TestGeminiFamilyAdapterConformanceLifecycleFixture(t *testing.T) {
	fake := writeFakeGeminiCLI(t)
	for _, tc := range []struct {
		id, name string
	}{
		{"fake-gemini", "Fake Gemini"},
		{"fake-qwen-code", "Fake Qwen Code"},
		{"fake-iflow", "Fake iFlow"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			adaptertest.CheckSessionLifecycle(t, NewWithCLI(tc.id, tc.name, fake), adaptertest.SessionContract{})
		})
	}
}

func writeFakeGeminiCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "fake-gemini"
	script := `#!/bin/sh
printf '%s\n' '{"type":"init","session_id":"fake-gemini-1","model":"fake-model"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"fake hello"}]}}'
printf '%s\n' '{"type":"result","result":"fake done","is_error":false,"usage":{"input_tokens":1,"output_tokens":2}}'
`
	if runtime.GOOS == "windows" {
		name += ".bat"
		script = `@echo off
echo {"type":"init","session_id":"fake-gemini-1","model":"fake-model"}
echo {"type":"assistant","message":{"content":[{"type":"text","text":"fake hello"}]}}
echo {"type":"result","result":"fake done","is_error":false,"usage":{"input_tokens":1,"output_tokens":2}}
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
