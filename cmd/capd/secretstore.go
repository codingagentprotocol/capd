package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/account/secret"
)

func newSecretStoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secretstore",
		Short: "Check local SecretStore backend readiness",
	}
	checkCmd := &cobra.Command{
		Use:   "check",
		Short: "Verify the configured SecretStore backend",
		RunE: func(cmd *cobra.Command, _ []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			roundTrip, _ := cmd.Flags().GetBool("roundtrip")
			backend, _ := cmd.Flags().GetString("secret-backend")
			requireBackend, _ := cmd.Flags().GetString("require-backend")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			backend, err := secret.NormalizeBackend(backend)
			if err != nil {
				return err
			}
			requireBackend, err = secret.NormalizeBackend(requireBackend)
			if err != nil {
				return err
			}
			checkCtx := cmd.Context()
			var cancel context.CancelFunc
			if timeout > 0 {
				checkCtx, cancel = context.WithTimeout(checkCtx, timeout)
				defer cancel()
			}
			report, err := buildSecretStoreReport(checkCtx, secretStoreOptions{
				Backend:        backend,
				RequireBackend: requireBackend,
				RoundTrip:      roundTrip,
			})
			if err != nil {
				return err
			}
			if jsonOut {
				out, _ := json.MarshalIndent(report, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
			} else {
				printSecretStoreReport(cmd, report)
			}
			if !report.OK {
				cmd.SilenceUsage = true
				cmd.SilenceErrors = true
				return fmt.Errorf("secretstore check failed")
			}
			return nil
		},
	}
	checkCmd.Flags().Bool("json", false, "print safe SecretStore readiness evidence as JSON")
	checkCmd.Flags().Bool("roundtrip", false, "write, read, and delete a diagnostic secret in the selected backend")
	checkCmd.Flags().String("secret-backend", "", "SecretStore backend to open (file or native; default CAPD_SECRET_BACKEND/file)")
	checkCmd.Flags().String("require-backend", "", "fail unless the selected backend is this value (file or native)")
	checkCmd.Flags().Duration("timeout", 2*time.Minute, "maximum time to wait for SecretStore backend checks")
	cmd.AddCommand(checkCmd)
	return cmd
}

type secretStoreOptions struct {
	Backend        string
	RequireBackend string
	RoundTrip      bool
}

type secretStoreReport struct {
	OK              bool               `json:"ok"`
	Backend         string             `json:"backend"`
	RequiredBackend string             `json:"requiredBackend,omitempty"`
	RoundTrip       *secretStoreCheck  `json:"roundtrip,omitempty"`
	Checks          []secretStoreCheck `json:"checks"`
	Issues          []string           `json:"issues,omitempty"`
	NextSteps       []string           `json:"nextSteps,omitempty"`
}

type secretStoreCheck struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Evidence string `json:"evidence"`
	NextStep string `json:"nextStep,omitempty"`
}

func buildSecretStoreReport(ctx context.Context, opts secretStoreOptions) (secretStoreReport, error) {
	accounts, secrets, err := openAccountDepsWithBackend(opts.Backend)
	if err != nil {
		return secretStoreReport{}, err
	}
	defer accounts.Close()
	report := secretStoreReport{
		Backend:         secrets.Backend(),
		RequiredBackend: opts.RequireBackend,
	}
	backendOK := opts.RequireBackend == "" || secrets.Backend() == opts.RequireBackend
	if !backendOK {
		report.Issues = append(report.Issues, fmt.Sprintf("secret backend is %q, want %q", secrets.Backend(), opts.RequireBackend))
		report.NextSteps = append(report.NextSteps, secretStoreBackendMismatchNextStep(opts.RequireBackend))
	}
	report.Checks = append(report.Checks, secretStoreCheck{
		Name:     "SecretStore backend",
		OK:       backendOK,
		Evidence: "secret backend " + secrets.Backend(),
		NextStep: secretStoreNextStep(!backendOK, secretStoreBackendMismatchNextStep(opts.RequireBackend)),
	})
	if opts.RoundTrip {
		check := secretStoreCheck{
			Name:     "SecretStore roundtrip",
			OK:       true,
			Evidence: "roundtrip ok for backend " + secrets.Backend(),
		}
		if err := doctorSecretStoreRoundTrip(ctx, secrets); err != nil {
			check.OK = false
			check.Evidence = "roundtrip failed for backend " + secrets.Backend()
			check.NextStep = "verify native SecretStore support with: make verify-secretstore"
			report.Issues = append(report.Issues, "SecretStore roundtrip failed")
			report.NextSteps = append(report.NextSteps, check.NextStep)
		}
		report.RoundTrip = &check
		report.Checks = append(report.Checks, check)
	}
	report.Issues = compactStrings(report.Issues)
	report.NextSteps = compactStrings(report.NextSteps)
	report.OK = len(report.Issues) == 0
	return report, nil
}

func printSecretStoreReport(cmd *cobra.Command, report secretStoreReport) {
	status := "ok"
	if !report.OK {
		status = "needs attention"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "secretstore: %s\n", status)
	fmt.Fprintf(cmd.OutOrStdout(), "backend: %s\n", report.Backend)
	for _, check := range report.Checks {
		state := "fail"
		if check.OK {
			state = "pass"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s - %s\n", check.Name, state, check.Evidence)
		if check.NextStep != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "next: %s\n", check.NextStep)
		}
	}
}

func secretStoreNextStep(include bool, step string) string {
	if include {
		return step
	}
	return ""
}

func secretStoreBackendMismatchNextStep(requireBackend string) string {
	if requireBackend == "" {
		return "restart or rerun with the required SecretStore backend"
	}
	return "restart or rerun with: capd secretstore check --secret-backend " + requireBackend + " --require-backend " + requireBackend + " --timeout 2m"
}
