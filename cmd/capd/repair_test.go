package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/codingagentprotocol/capd/internal/audit"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestRepairRunDryRunClassifiesManualAndRunnableSteps(t *testing.T) {
	steps := []protocol.RepairStep{
		{ID: "start-daemon", Title: "Start daemon", Command: "capd start --secret-backend native"},
		{ID: "import-codex-accounts", Title: "Import accounts", Command: "capd accounts import --auth /path/a/auth.json --auth /path/b/auth.json"},
		{ID: "refresh-quota-readiness", Title: "Refresh quota", Command: "capd accounts check --json --readiness --timeout 2m"},
		{ID: "final-live-preflight", Title: "Final preflight", Command: "make live-codex-preflight"},
		{ID: "unknown", Title: "Unknown", Command: "python repair.py"},
	}
	report, err := runRepairPlan(context.Background(), steps, repairRunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || !report.DryRun || report.Summary.Total != 5 || report.Summary.Planned != 1 || report.Summary.Skipped != 4 {
		t.Fatalf("report = %+v", report)
	}
	if report.Steps[0].Status != "skipped" || !strings.Contains(report.Steps[0].Reason, "foreground daemon") {
		t.Fatalf("daemon step = %+v", report.Steps[0])
	}
	if report.Steps[1].Status != "skipped" || !strings.Contains(report.Steps[1].Reason, "placeholders") {
		t.Fatalf("placeholder step = %+v", report.Steps[1])
	}
	if report.Steps[2].Status != "planned" {
		t.Fatalf("quota step = %+v", report.Steps[2])
	}
	if report.Steps[3].Status != "skipped" || !strings.Contains(report.Steps[3].Reason, "--include-final") {
		t.Fatalf("final step = %+v", report.Steps[3])
	}
	if report.Steps[4].Status != "skipped" || !strings.Contains(report.Steps[4].Reason, "allowlist") {
		t.Fatalf("unknown step = %+v", report.Steps[4])
	}
}

func TestRepairRunExecuteOnlyRunsRunnableSteps(t *testing.T) {
	restore := stubRepairExecCommand(t)
	defer restore()
	steps := []protocol.RepairStep{
		{ID: "import-codex-accounts", Title: "Import accounts", Command: "capd accounts import --auth /path/a/auth.json"},
		{ID: "refresh-quota-readiness", Title: "Refresh quota", Command: "capd accounts check --json --readiness --timeout 2m"},
	}
	report, err := runRepairPlan(context.Background(), steps, repairRunOptions{Execute: true, Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || report.DryRun || report.Summary.Succeeded != 1 || report.Summary.Skipped != 1 || report.Summary.Failed != 0 {
		t.Fatalf("report = %+v", report)
	}
	if report.Steps[0].Status != "skipped" {
		t.Fatalf("placeholder step = %+v", report.Steps[0])
	}
	if report.Steps[1].Status != "succeeded" || !strings.Contains(report.Steps[1].Output, "capd accounts check --json --readiness --timeout 2m") {
		t.Fatalf("executed step = %+v", report.Steps[1])
	}
}

func TestRepairRunExecuteRequiresConfirmation(t *testing.T) {
	restore := stubRepairExecCommand(t)
	defer restore()
	_, err := runRepairPlan(context.Background(), []protocol.RepairStep{
		{ID: "refresh-quota-readiness", Title: "Refresh quota", Command: "capd accounts check --json --readiness --timeout 2m"},
	}, repairRunOptions{Execute: true, Stdin: strings.NewReader("no\n"), Stdout: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("expected cancel error, got %v", err)
	}
}

func TestRepairRunCommandPrintsJSONDryRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAPD_HOST", "127.0.0.1")
	t.Setenv("CAPD_PORT", "1")
	if err := writeTokenForTest(home, "tok-repair-json"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := newRepairCmd()
	cmd.SetArgs([]string{"run", "--json", "--require-secret-backend", "native"})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{`"dryRun": true`, `"status": "skipped"`, `"command": "capd start --secret-backend native"`, `"command": "make live-codex-preflight"`, `"execution": {`, `"runnable": false`, `"reason": "starts a foreground daemon; run manually in another terminal"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("repair JSON missing %q: %s", want, text)
		}
	}
	for _, forbidden := range []string{"tok-repair-json", home} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("repair JSON leaked %q: %s", forbidden, text)
		}
	}
	events, err := audit.Recent("", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "repair.run" || events[0].Outcome != "ok" || events[0].Data["dryRun"] != true || events[0].Data["total"] == float64(0) {
		t.Fatalf("audit events = %+v", events)
	}
	if strings.Contains(fmt.Sprint(events), "tok-repair-json") || strings.Contains(fmt.Sprint(events), home) || strings.Contains(fmt.Sprint(events), "capd start") {
		t.Fatalf("repair audit leaked sensitive or command detail: %+v", events)
	}
}

func stubRepairExecCommand(t *testing.T) func() {
	t.Helper()
	orig := repairExecCommand
	repairExecCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		all := append([]string{"-test.run=TestRepairExecHelper", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], all...)
		cmd.Env = append(os.Environ(), "CAPD_REPAIR_EXEC_HELPER=1")
		return cmd
	}
	return func() {
		repairExecCommand = orig
	}
}

func TestRepairExecHelper(t *testing.T) {
	if os.Getenv("CAPD_REPAIR_EXEC_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Stdout.WriteString(strings.Join(os.Args[i+1:], " "))
			os.Exit(0)
		}
	}
	os.Exit(2)
}
