package repairrunner

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/codingagentprotocol/capd/internal/proc"
	"github.com/codingagentprotocol/capd/internal/repairplan"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

type Options struct {
	Execute      bool
	IncludeFinal bool
}

type ExecFunc func(context.Context, proc.Spec) (string, error)

const (
	MaxRepairSteps       = 20
	MaxRepairCommandSize = 4096
	MaxRepairOutputSize  = 8192
)

func Run(ctx context.Context, steps []protocol.RepairStep, opts Options, execFn ExecFunc) protocol.RepairRunResult {
	if execFn == nil {
		execFn = proc.RunCombined
	}
	capacity := len(steps)
	if capacity > MaxRepairSteps {
		capacity = MaxRepairSteps
	}
	result := protocol.RepairRunResult{
		OK:     true,
		DryRun: !opts.Execute,
		Steps:  make([]protocol.RepairRunStepReport, 0, capacity),
		Summary: protocol.RepairRunSummary{
			Total: len(steps),
		},
	}
	for i, step := range steps {
		if i >= MaxRepairSteps {
			result.Summary.Skipped++
			continue
		}
		execution := repairplan.Classify(step, repairplan.Options{IncludeFinal: opts.IncludeFinal})
		if len(step.Command) > MaxRepairCommandSize {
			execution.Runnable = false
			execution.Reason = "command exceeds repair runner limit"
		}
		step.Execution = &execution
		row := protocol.RepairRunStepReport{Step: step}
		if !opts.Execute {
			if execution.Runnable {
				row.Status = "planned"
				row.Reason = "use execute=true to run"
				result.Summary.Planned++
			} else {
				row.Status = "skipped"
				row.Reason = execution.Reason
				result.Summary.Skipped++
			}
			result.Steps = append(result.Steps, row)
			continue
		}
		if !execution.Runnable {
			row.Status = "skipped"
			row.Reason = execution.Reason
			result.Summary.Skipped++
			result.Steps = append(result.Steps, row)
			continue
		}
		env, bin, args := repairplan.SplitCommand(step.Command)
		output, err := execFn(ctx, proc.Spec{Bin: bin, Args: args, Env: env})
		row.Output, row.OutputTruncated = boundedOutput(output)
		if err != nil {
			row.Status = "failed"
			row.Error = err.Error()
			result.OK = false
			result.Summary.Failed++
		} else {
			row.Status = "succeeded"
			result.Summary.Succeeded++
		}
		result.Steps = append(result.Steps, row)
	}
	return result
}

func boundedOutput(output string) (string, bool) {
	output = strings.TrimSpace(output)
	if len(output) <= MaxRepairOutputSize {
		return output, false
	}
	trimmed := output[:MaxRepairOutputSize]
	for !utf8.ValidString(trimmed) {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return strings.TrimSpace(trimmed), true
}
