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

func TestRunBoundsExecutedStepOutput(t *testing.T) {
	got := Run(context.Background(), []protocol.RepairStep{
		{ID: "runnable", Command: "capd accounts check --json"},
	}, Options{Execute: true}, func(_ context.Context, _ proc.Spec) (string, error) {
		return strings.Repeat("a", MaxRepairOutputSize+100), nil
	})
	if !got.OK || got.Summary.Succeeded != 1 || len(got.Steps) != 1 {
		t.Fatalf("run = %+v", got)
	}
	if !got.Steps[0].OutputTruncated || len(got.Steps[0].Output) > MaxRepairOutputSize {
		t.Fatalf("output len=%d truncated=%v", len(got.Steps[0].Output), got.Steps[0].OutputTruncated)
	}
}

func TestRunBoundsStepCountAndCommandSize(t *testing.T) {
	steps := make([]protocol.RepairStep, 0, MaxRepairSteps+2)
	for i := 0; i < MaxRepairSteps+2; i++ {
		steps = append(steps, protocol.RepairStep{ID: "step", Command: "capd accounts check --json"})
	}
	steps[0].Command = "capd " + strings.Repeat("x", MaxRepairCommandSize)
	got := Run(context.Background(), steps, Options{Execute: true}, func(_ context.Context, _ proc.Spec) (string, error) {
		return "ok", nil
	})
	if got.Summary.Total != MaxRepairSteps+2 || len(got.Steps) != MaxRepairSteps {
		t.Fatalf("run = %+v", got)
	}
	if got.Summary.Skipped != 3 || got.Summary.Succeeded != MaxRepairSteps-1 {
		t.Fatalf("summary = %+v", got.Summary)
	}
	if got.Steps[0].Status != "skipped" || got.Steps[0].Reason != "command exceeds repair runner limit" {
		t.Fatalf("first step = %+v", got.Steps[0])
	}
}
