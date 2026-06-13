package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
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
			recordSecretStoreCheckAudit(report)
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
			check.NextStep = secretStoreRoundTripNextStep(secrets.Backend(), err)
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

func secretStoreRoundTripNextStep(backend string, err error) string {
	rerun := "capd secretstore check --secret-backend " + backend + " --roundtrip --require-backend " + backend + " --timeout 2m"
	if backend != secret.BackendNative {
		return "rerun SecretStore roundtrip with: " + rerun
	}
	text := strings.ToLower(fmt.Sprint(err))
	switch {
	case strings.Contains(text, "keychain") || strings.Contains(text, "status -128"):
		return "approve macOS Keychain access, then rerun: " + rerun + " (or restart with file SecretStore and re-import accounts for no-prompt local testing)"
	case strings.Contains(text, "secret-tool") || strings.Contains(text, "secret service"):
		return "install libsecret secret-tool and unlock the Linux Secret Service/keyring, then rerun: " + rerun
	case strings.Contains(text, "credential"):
		return "check Windows Credential Manager availability for the current user, then rerun: " + rerun
	case errors.Is(err, secret.ErrNativeUnavailable):
		return "verify native SecretStore support with: make verify-secretstore"
	case runtime.GOOS == "darwin":
		return "approve macOS Keychain access, then rerun: " + rerun + " (or restart with file SecretStore and re-import accounts for no-prompt local testing)"
	case runtime.GOOS == "linux":
		return "install libsecret secret-tool and unlock the Linux Secret Service/keyring, then rerun: " + rerun
	case runtime.GOOS == "windows":
		return "check Windows Credential Manager availability for the current user, then rerun: " + rerun
	default:
		return "verify native SecretStore support with: make verify-secretstore, then rerun: " + rerun
	}
}
