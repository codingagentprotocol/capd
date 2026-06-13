package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/proc"
	"github.com/codingagentprotocol/capd/internal/repairplan"
	"github.com/codingagentprotocol/capd/internal/repairrunner"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

var repairExecCommand = func(ctx context.Context, spec proc.Spec) (string, error) {
	cmd := exec.CommandContext(ctx, spec.Bin, spec.Args...)
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func newRepairCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Plan and safely run readiness repair steps",
	}
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Dry-run or execute safe doctor repair steps",
		RunE: func(cmd *cobra.Command, _ []string) error {
			execute, _ := cmd.Flags().GetBool("execute")
			yes, _ := cmd.Flags().GetBool("yes")
			jsonOut, _ := cmd.Flags().GetBool("json")
			includeFinal, _ := cmd.Flags().GetBool("include-final")
			verifySecretStore, _ := cmd.Flags().GetBool("verify-secretstore")
			promptFree, _ := cmd.Flags().GetBool("prompt-free")
			requireSecretBackend, _ := cmd.Flags().GetString("require-secret-backend")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			requireSecretBackend, err := secret.NormalizeBackend(requireSecretBackend)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			var cancel context.CancelFunc
			if timeout > 0 {
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			report, err := buildDoctorReport(ctx, doctorOptions{
				RequireSecretBackend: requireSecretBackend,
				VerifySecretStore:    verifySecretStore,
				PromptFree:           promptFree,
			})
			if err != nil {
				return err
			}
			run, err := runRepairPlan(ctx, report.RepairPlan, repairRunOptions{
				Execute:      execute,
				Yes:          yes,
				IncludeFinal: includeFinal,
				Stdin:        cmd.InOrStdin(),
				Stdout:       cmd.OutOrStdout(),
				Stderr:       cmd.ErrOrStderr(),
			})
			if err != nil {
				return err
			}
			recordRepairRunAudit(run)
			if jsonOut {
				out, _ := json.MarshalIndent(run, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
			} else {
				printRepairRunReport(cmd.OutOrStdout(), run)
			}
			if !run.OK {
				return fmt.Errorf("repair run failed")
			}
			return nil
		},
	}
	runCmd.Flags().Bool("execute", false, "execute runnable repair steps; default is dry-run only")
	runCmd.Flags().Bool("yes", false, "skip confirmation when used with --execute")
	runCmd.Flags().Bool("json", false, "print machine-readable repair run results")
	runCmd.Flags().Bool("include-final", false, "allow executing the final live preflight step")
	runCmd.Flags().Bool("verify-secretstore", false, "let doctor verify SecretStore with a diagnostic roundtrip before planning")
	runCmd.Flags().Bool("prompt-free", true, "skip account SecretStore credential reads while building the repair plan")
	runCmd.Flags().String("require-secret-backend", "", "plan against a required SecretStore backend (file or native)")
	runCmd.Flags().Duration("timeout", 2*time.Minute, "maximum time for planning and executed repair commands")
	cmd.AddCommand(runCmd)
	return cmd
}

type repairRunOptions struct {
	Execute      bool
	Yes          bool
	IncludeFinal bool
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
}

type repairRunReport struct {
	OK      bool                  `json:"ok"`
	DryRun  bool                  `json:"dryRun"`
	Steps   []repairRunStepReport `json:"steps"`
	Summary repairRunSummary      `json:"summary"`
}

type repairRunSummary struct {
	Total     int `json:"total"`
	Planned   int `json:"planned,omitempty"`
	Skipped   int `json:"skipped,omitempty"`
	Succeeded int `json:"succeeded,omitempty"`
	Failed    int `json:"failed,omitempty"`
}

type repairRunStepReport struct {
	Step            protocol.RepairStep `json:"step"`
	Status          string              `json:"status"`
	Reason          string              `json:"reason,omitempty"`
	Output          string              `json:"output,omitempty"`
	OutputTruncated bool                `json:"outputTruncated,omitempty"`
	Error           string              `json:"error,omitempty"`
}

func runRepairPlan(ctx context.Context, steps []protocol.RepairStep, opts repairRunOptions) (repairRunReport, error) {
	if opts.Execute && !opts.Yes {
		if !confirmRepairRun(opts.Stdin, opts.Stdout, len(steps)) {
			return repairRunReport{OK: true, DryRun: !opts.Execute, Summary: repairRunSummary{Total: len(steps)}}, fmt.Errorf("repair execution canceled")
		}
	}
	run := repairrunner.Run(ctx, steps, repairrunner.Options{Execute: opts.Execute, IncludeFinal: opts.IncludeFinal}, repairExecCommand)
	return repairRunReport{
		OK:     run.OK,
		DryRun: run.DryRun,
		Steps:  repairRunStepReports(run.Steps),
		Summary: repairRunSummary{
			Total:     run.Summary.Total,
			Planned:   run.Summary.Planned,
			Skipped:   run.Summary.Skipped,
			Succeeded: run.Summary.Succeeded,
			Failed:    run.Summary.Failed,
		},
	}, nil
}

func repairStepDryRunStatus(step protocol.RepairStep, opts repairRunOptions) (string, string) {
	classification := repairStepClassification(step, opts)
	if !classification.Runnable {
		return "skipped", classification.Reason
	}
	return "planned", "use --execute --yes to run"
}

func repairStepClassification(step protocol.RepairStep, opts repairRunOptions) protocol.RepairStepExecution {
	return repairplan.Classify(step, repairplan.Options{IncludeFinal: opts.IncludeFinal})
}

func repairStepWithExecution(step protocol.RepairStep, opts repairRunOptions) protocol.RepairStep {
	execution := repairStepClassification(step, opts)
	step.Execution = &execution
	return step
}

func repairRunStepReports(steps []protocol.RepairRunStepReport) []repairRunStepReport {
	out := make([]repairRunStepReport, 0, len(steps))
	for _, step := range steps {
		out = append(out, repairRunStepReport{
			Step:            step.Step,
			Status:          step.Status,
			Reason:          step.Reason,
			Output:          step.Output,
			OutputTruncated: step.OutputTruncated,
			Error:           step.Error,
		})
	}
	return out
}

func confirmRepairRun(stdin io.Reader, stdout io.Writer, count int) bool {
	if stdin == nil {
		stdin = os.Stdin
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	fmt.Fprintf(stdout, "About to execute runnable repair steps from %d planned step(s). Type execute to continue: ", count)
	scanner := bufio.NewScanner(stdin)
	if !scanner.Scan() {
		return false
	}
	return strings.TrimSpace(scanner.Text()) == "execute"
}

func printRepairRunReport(w io.Writer, report repairRunReport) {
	mode := "dry-run"
	if !report.DryRun {
		mode = "execute"
	}
	fmt.Fprintf(w, "repair %s: total=%d planned=%d skipped=%d succeeded=%d failed=%d\n", mode, report.Summary.Total, report.Summary.Planned, report.Summary.Skipped, report.Summary.Succeeded, report.Summary.Failed)
	for i, step := range report.Steps {
		fmt.Fprintf(w, "%d. %s [%s]\n", i+1, step.Step.Title, step.Status)
		if step.Step.Command != "" {
			fmt.Fprintf(w, "   command: %s\n", step.Step.Command)
		}
		if step.Reason != "" {
			fmt.Fprintf(w, "   reason: %s\n", step.Reason)
		}
		if step.Error != "" {
			fmt.Fprintf(w, "   error: %s\n", step.Error)
		}
		if step.Output != "" {
			fmt.Fprintf(w, "   output: %s\n", step.Output)
			if step.OutputTruncated {
				fmt.Fprintln(w, "   output truncated: true")
			}
		}
	}
}

func printRepairPlanText(w io.Writer, steps []protocol.RepairStep) {
	if len(steps) == 0 {
		return
	}
	fmt.Fprintln(w, "repair plan:")
	for i, step := range steps {
		fmt.Fprintf(w, "%d. %s\n", i+1, step.Title)
		fmt.Fprintf(w, "   command: %s\n", step.Command)
		if step.Execution != nil {
			label := "manual"
			if step.Execution.Runnable {
				label = "runnable"
			}
			fmt.Fprintf(w, "   execution: %s", label)
			if step.Execution.Reason != "" {
				fmt.Fprintf(w, " (%s)", step.Execution.Reason)
			}
			fmt.Fprintln(w)
		}
		if step.ExpectedEvidence != "" {
			fmt.Fprintf(w, "   expect: %s\n", step.ExpectedEvidence)
		}
	}
}
