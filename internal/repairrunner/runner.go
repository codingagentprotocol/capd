package repairrunner

import (
	"context"
	"strings"

	"github.com/codingagentprotocol/capd/internal/proc"
	"github.com/codingagentprotocol/capd/internal/repairplan"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

type Options struct {
	Execute      bool
	IncludeFinal bool
}

type ExecFunc func(context.Context, proc.Spec) (string, error)

func Run(ctx context.Context, steps []protocol.RepairStep, opts Options, execFn ExecFunc) protocol.RepairRunResult {
	if execFn == nil {
		execFn = proc.RunCombined
	}
	result := protocol.RepairRunResult{
		OK:     true,
		DryRun: !opts.Execute,
		Steps:  make([]protocol.RepairRunStepReport, 0, len(steps)),
		Summary: protocol.RepairRunSummary{
			Total: len(steps),
		},
	}
	for _, step := range steps {
		execution := repairplan.Classify(step, repairplan.Options{IncludeFinal: opts.IncludeFinal})
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
		row.Output = strings.TrimSpace(output)
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
