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
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

var repairExecCommand = exec.CommandContext

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
	Step   protocol.RepairStep `json:"step"`
	Status string              `json:"status"`
	Reason string              `json:"reason,omitempty"`
	Output string              `json:"output,omitempty"`
	Error  string              `json:"error,omitempty"`
}

func runRepairPlan(ctx context.Context, steps []protocol.RepairStep, opts repairRunOptions) (repairRunReport, error) {
	out := repairRunReport{
		OK:     true,
		DryRun: !opts.Execute,
		Steps:  make([]repairRunStepReport, 0, len(steps)),
		Summary: repairRunSummary{
			Total: len(steps),
		},
	}
	if !opts.Execute {
		for _, step := range steps {
			status, reason := repairStepDryRunStatus(step, opts)
			out.Steps = append(out.Steps, repairRunStepReport{Step: step, Status: status, Reason: reason})
			if status == "planned" {
				out.Summary.Planned++
			} else {
				out.Summary.Skipped++
			}
		}
		return out, nil
	}
	if !opts.Yes {
		if !confirmRepairRun(opts.Stdin, opts.Stdout, len(steps)) {
			return out, fmt.Errorf("repair execution canceled")
		}
	}
	for _, step := range steps {
		runnable, reason := repairStepRunnable(step, opts)
		row := repairRunStepReport{Step: step}
		if !runnable {
			row.Status = "skipped"
			row.Reason = reason
			out.Summary.Skipped++
			out.Steps = append(out.Steps, row)
			continue
		}
		output, err := executeRepairStep(ctx, step)
		row.Output = strings.TrimSpace(output)
		if err != nil {
			row.Status = "failed"
			row.Error = err.Error()
			out.OK = false
			out.Summary.Failed++
		} else {
			row.Status = "succeeded"
			out.Summary.Succeeded++
		}
		out.Steps = append(out.Steps, row)
	}
	return out, nil
}

func repairStepDryRunStatus(step protocol.RepairStep, opts repairRunOptions) (string, string) {
	if ok, reason := repairStepRunnable(step, opts); !ok {
		return "skipped", reason
	}
	return "planned", "use --execute --yes to run"
}

func repairStepRunnable(step protocol.RepairStep, opts repairRunOptions) (bool, string) {
	command := strings.TrimSpace(step.Command)
	if command == "" {
		return false, "empty command"
	}
	if strings.Contains(command, "/path/") || strings.Contains(command, "<") || strings.Contains(command, "...") {
		return false, "command contains placeholders"
	}
	if strings.HasPrefix(command, "capd start") {
		return false, "starts a foreground daemon; run manually in another terminal"
	}
	if strings.HasPrefix(command, "export ") {
		return false, "changes shell environment; run manually in your shell"
	}
	_, bin, _ := splitRepairCommand(command)
	if bin != "capd" && bin != "make" {
		return false, "command is outside the repair runner allowlist"
	}
	if step.ID == "final-live-preflight" && !opts.IncludeFinal {
		return false, "final live preflight requires --include-final"
	}
	return true, ""
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

func executeRepairStep(ctx context.Context, step protocol.RepairStep) (string, error) {
	env, bin, args := splitRepairCommand(step.Command)
	if bin == "" {
		return "", fmt.Errorf("empty command")
	}
	cmd := repairExecCommand(ctx, bin, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func splitRepairCommand(command string) ([]string, string, []string) {
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
		}
	}
}
