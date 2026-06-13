package repairplan

import (
	"strings"

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// Options controls conservative repair runner classification.
type Options struct {
	IncludeFinal bool
}

// Annotate returns a copy of steps with execution classification attached.
func Annotate(steps []protocol.RepairStep, opts Options) []protocol.RepairStep {
	out := make([]protocol.RepairStep, 0, len(steps))
	for _, step := range steps {
		execution := Classify(step, opts)
		step.Execution = &execution
		out = append(out, step)
	}
	return out
}

// Classify applies the same conservative policy used by the CLI repair runner.
func Classify(step protocol.RepairStep, opts Options) protocol.RepairStepExecution {
	command := strings.TrimSpace(step.Command)
	if command == "" {
		return protocol.RepairStepExecution{Runnable: false, Reason: "empty command"}
	}
	if strings.Contains(command, "/path/") || strings.Contains(command, "<") || strings.Contains(command, "...") {
		return protocol.RepairStepExecution{Runnable: false, Reason: "command contains placeholders"}
	}
	if strings.HasPrefix(command, "capd start") {
		return protocol.RepairStepExecution{Runnable: false, Reason: "starts a foreground daemon; run manually in another terminal"}
	}
	if strings.HasPrefix(command, "export ") {
		return protocol.RepairStepExecution{Runnable: false, Reason: "changes shell environment; run manually in your shell"}
	}
	_, bin, _ := SplitCommand(command)
	if bin != "capd" && bin != "make" {
		return protocol.RepairStepExecution{Runnable: false, Reason: "command is outside the repair runner allowlist"}
	}
	if step.ID == "final-live-preflight" && !opts.IncludeFinal {
		return protocol.RepairStepExecution{Runnable: false, Reason: "final live preflight requires --include-final"}
	}
	return protocol.RepairStepExecution{Runnable: true, Reason: "safe repair runner allowlist"}
}

// SplitCommand extracts leading VAR=value env overrides, the binary, and args.
func SplitCommand(command string) ([]string, string, []string) {
	fields := strings.Fields(command)
	env := []string{}
	for len(fields) > 0 && strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "-") {
		env = append(env, fields[0])
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return env, "", nil
	}
	return env, fields[0], fields[1:]
}
