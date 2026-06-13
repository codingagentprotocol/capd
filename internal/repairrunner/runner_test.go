package repairrunner

import (
	"context"
	"strings"
	"testing"

	"github.com/codingagentprotocol/capd/internal/proc"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestRunDryRunClassifiesSteps(t *testing.T) {
	got := Run(context.Background(), []protocol.RepairStep{
		{ID: "manual", Command: "capd accounts import --auth /path/a/auth.json"},
		{ID: "runnable", Command: "capd accounts check --json --readiness --timeout 2m"},
	}, Options{}, nil)
	if !got.OK || !got.DryRun || got.Summary.Total != 2 || got.Summary.Planned != 1 || got.Summary.Skipped != 1 {
		t.Fatalf("run = %+v", got)
	}
	if got.Steps[0].Status != "skipped" || got.Steps[1].Status != "planned" {
		t.Fatalf("steps = %+v", got.Steps)
	}
}

func TestRunExecuteOnlyRunnableSteps(t *testing.T) {
	calls := []string{}
	got := Run(context.Background(), []protocol.RepairStep{
		{ID: "manual", Command: "capd accounts import --auth /path/a/auth.json"},
		{ID: "runnable", Command: "CAPD_SECRET_BACKEND=native capd accounts check --json"},
	}, Options{Execute: true}, func(_ context.Context, spec proc.Spec) (string, error) {
		calls = append(calls, strings.Join(append([]string{spec.Bin}, spec.Args...), " "))
		if len(spec.Env) != 1 || spec.Env[0] != "CAPD_SECRET_BACKEND=native" {
			t.Fatalf("env = %+v", spec.Env)
		}
		return "ok", nil
	})
	if !got.OK || got.DryRun || got.Summary.Succeeded != 1 || got.Summary.Skipped != 1 || len(calls) != 1 || calls[0] != "capd accounts check --json" {
		t.Fatalf("run = %+v calls=%v", got, calls)
	}
}
