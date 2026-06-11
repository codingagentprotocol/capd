package adapter

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// ProbeCLI is the shared probe implementation: resolve the binary on PATH and
// ask it for its version. Used by every built-in adapter.
func ProbeCLI(ctx context.Context, id, name, bin string, versionArgs ...string) (protocol.AgentInfo, error) {
	info := protocol.AgentInfo{ID: id, Name: name}

	path, err := exec.LookPath(bin)
	if err != nil {
		return info, nil // not installed — not an error
	}
	info.Bin = path
	info.Available = true

	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(vctx, path, versionArgs...).Output()
	if err == nil {
		// Keep the first line only; some CLIs print banners.
		line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
		info.Version = strings.TrimSpace(line)
	}
	return info, nil
}

// RequireBin fails fast when the CLI is not on PATH — session creation for a
// missing agent should error immediately, not at first Send.
func RequireBin(id, bin string) error {
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("agent %q is not installed (%s not on PATH)", id, bin)
	}
	return nil
}
