package repairplan

import (
	"strings"
	"testing"

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestClassifyRepairStep(t *testing.T) {
	tests := []struct {
		name     string
		step     protocol.RepairStep
		opts     Options
		runnable bool
		reason   string
	}{
		{name: "empty", step: protocol.RepairStep{}, reason: "empty command"},
		{name: "placeholder", step: protocol.RepairStep{Command: "capd accounts import --auth /path/a/auth.json"}, reason: "placeholders"},
		{name: "daemon", step: protocol.RepairStep{Command: "capd start --secret-backend native"}, reason: "foreground daemon"},
		{name: "export", step: protocol.RepairStep{Command: "export CAPD_SECRET_BACKEND=native"}, reason: "shell environment"},
		{name: "allowlist", step: protocol.RepairStep{Command: "python repair.py"}, reason: "allowlist"},
		{name: "final skipped", step: protocol.RepairStep{ID: "final-live-preflight", Command: "make live-codex-preflight"}, reason: "--include-final"},
		{name: "final included", step: protocol.RepairStep{ID: "final-live-preflight", Command: "make live-codex-preflight"}, opts: Options{IncludeFinal: true}, runnable: true, reason: "safe repair runner allowlist"},
		{name: "capd runnable", step: protocol.RepairStep{Command: "capd accounts check --json --readiness --timeout 2m"}, runnable: true, reason: "safe repair runner allowlist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.step, tt.opts)
			if got.Runnable != tt.runnable || !strings.Contains(got.Reason, tt.reason) {
				t.Fatalf("classification = %+v", got)
			}
		})
	}
}

func TestAnnotateRepairSteps(t *testing.T) {
	got := Annotate([]protocol.RepairStep{{Command: "capd accounts check --json"}}, Options{})
	if len(got) != 1 || got[0].Execution == nil || !got[0].Execution.Runnable {
		t.Fatalf("annotated = %+v", got)
	}
}
