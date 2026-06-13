package server

import (
	"context"

	"github.com/codingagentprotocol/capd/internal/audit"
	"github.com/codingagentprotocol/capd/internal/proc"
	"github.com/codingagentprotocol/capd/internal/repairrunner"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

var serverRepairExecCommand = proc.RunCombined

func (s *Server) runRepair(ctx context.Context, params protocol.RepairRunParams) protocol.RepairRunResult {
	result := repairrunner.Run(ctx, params.Steps, repairrunner.Options{
		Execute:      params.Execute,
		IncludeFinal: params.IncludeFinal,
	}, serverRepairExecCommand)
	_ = audit.Append("", audit.Event{
		Type:    "repair.run",
		Actor:   "server",
		Outcome: repairRunOutcome(result),
		Data: map[string]any{
			"dryRun":       result.DryRun,
			"total":        result.Summary.Total,
			"planned":      result.Summary.Planned,
			"skipped":      result.Summary.Skipped,
			"succeeded":    result.Summary.Succeeded,
			"failed":       result.Summary.Failed,
			"includeFinal": params.IncludeFinal,
		},
	})
	return result
}

func repairRunOutcome(result protocol.RepairRunResult) string {
	if result.OK {
		return "ok"
	}
	return "failed"
}
