package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/audit"
	"github.com/codingagentprotocol/capd/internal/config"
	"github.com/codingagentprotocol/capd/internal/daemon"
	"github.com/codingagentprotocol/capd/internal/discovery"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func newSupportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "support",
		Short: "Collect safe local support evidence",
	}
	bundleCmd := &cobra.Command{
		Use:   "bundle",
		Short: "Write a redacted support evidence bundle",
		RunE: func(cmd *cobra.Command, _ []string) error {
			outDir, _ := cmd.Flags().GetString("out")
			requireSecretBackend, _ := cmd.Flags().GetString("require-secret-backend")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			includeProbeData, _ := cmd.Flags().GetBool("probe-data")
			fail, _ := cmd.Flags().GetBool("fail")
			requireSecretBackend, err := secret.NormalizeBackend(requireSecretBackend)
			if err != nil {
				return err
			}
			if strings.TrimSpace(outDir) == "" {
				outDir = filepath.Join(os.TempDir(), fmt.Sprintf("capd-support-%d", time.Now().Unix()))
			}
			callCtx := cmd.Context()
			var cancel context.CancelFunc
			if timeout > 0 {
				callCtx, cancel = context.WithTimeout(callCtx, timeout)
				defer cancel()
			}
			result, err := writeSupportBundle(callCtx, supportBundleOptions{
				OutDir:               outDir,
				RequireSecretBackend: requireSecretBackend,
				IncludeProbeData:     includeProbeData,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "bundle: %s\n", result.Dir)
			fmt.Fprintf(cmd.OutOrStdout(), "manifest: %s\n", result.ManifestPath)
			fmt.Fprintf(cmd.OutOrStdout(), "report: %s\n", result.ReportPath)
			fmt.Fprintf(cmd.OutOrStdout(), "ok: %t\n", result.Report.OK)
			for _, issue := range result.Report.Issues {
				fmt.Fprintf(cmd.OutOrStdout(), "issue: %s\n", issue)
			}
			if fail && !result.Report.OK {
				return fmt.Errorf("support bundle evidence failed: %s", strings.Join(result.Report.Issues, "; "))
			}
			return nil
		},
	}
	bundleCmd.Flags().String("out", "", "directory to write support evidence into (default: temp directory)")
	bundleCmd.Flags().String("require-secret-backend", "", "record an expected SecretStore backend for readiness evidence (file or native)")
	bundleCmd.Flags().Duration("timeout", 2*time.Minute, "maximum time to collect support evidence")
	bundleCmd.Flags().Bool("probe-data", true, "include authenticated /probe/data diagnostics when a daemon is reachable")
	bundleCmd.Flags().Bool("fail", false, "exit non-zero when the generated evidence report is not ready")
	cmd.AddCommand(bundleCmd)
	return cmd
}

type supportBundleOptions struct {
	OutDir               string
	RequireSecretBackend string
	IncludeProbeData     bool
}

type supportBundleResult struct {
	Dir          string              `json:"dir"`
	ManifestPath string              `json:"manifestPath"`
	ReportPath   string              `json:"reportPath"`
	Report       probeEvidenceReport `json:"report"`
}

func writeSupportBundle(ctx context.Context, opts supportBundleOptions) (supportBundleResult, error) {
	dir := filepath.Clean(opts.OutDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return supportBundleResult{}, err
	}
	artifacts := map[string]string{}
	cfg := config.Load()
	backend := opts.RequireSecretBackend

	healthPath := filepath.Join(dir, "health.json")
	health, err := daemonHealthInfo(ctx, cfg)
	if err != nil {
		if err := writeSupportJSON(healthPath, supportErrorArtifact("health", err)); err != nil {
			return supportBundleResult{}, err
		}
	} else {
		if backend == "" {
			backend = health.SecretBackend
		}
		if err := writeSupportJSON(healthPath, health); err != nil {
			return supportBundleResult{}, err
		}
	}
	artifacts["health"] = filepath.Base(healthPath)

	doctorPath := filepath.Join(dir, "doctor-prompt-free.json")
	doctor, err := buildDoctorReport(ctx, doctorOptions{
		RequireSecretBackend: backend,
		PromptFree:           true,
	})
	if err != nil {
		if err := writeSupportJSON(doctorPath, supportErrorArtifact("doctor", err)); err != nil {
			return supportBundleResult{}, err
		}
	} else {
		if backend == "" {
			backend = doctor.Summary.SecretBackend
		}
		if err := writeSupportJSON(doctorPath, doctor); err != nil {
			return supportBundleResult{}, err
		}
	}
	artifacts["doctor"] = filepath.Base(doctorPath)

	routePath := filepath.Join(dir, "agents-route.json")
	route, err := supportRouteEvidence(ctx)
	if err != nil {
		if err := writeSupportJSON(routePath, supportErrorArtifact("agents/route", err)); err != nil {
			return supportBundleResult{}, err
		}
	} else {
		if err := writeSupportJSON(routePath, route); err != nil {
			return supportBundleResult{}, err
		}
	}
	artifacts["agentsRoute"] = filepath.Base(routePath)

	auditPath := filepath.Join(dir, "audit.json")
	events, err := audit.Recent("", 100)
	if err != nil {
		if err := writeSupportJSON(auditPath, supportErrorArtifact("audit", err)); err != nil {
			return supportBundleResult{}, err
		}
	} else {
		if err := writeSupportJSON(auditPath, map[string]any{
			"ok":     true,
			"source": "audit",
			"events": events,
		}); err != nil {
			return supportBundleResult{}, err
		}
	}
	artifacts["audit"] = filepath.Base(auditPath)

	if opts.IncludeProbeData {
		probePath := filepath.Join(dir, "probe-data.json")
		body, _, err := daemonProbeData(ctx, cfg, probeDataOptions{})
		if err != nil {
			if err := writeSupportJSON(probePath, supportErrorArtifact("probe/data", err)); err != nil {
				return supportBundleResult{}, err
			}
		} else {
			if err := writeRawSupportJSON(probePath, body); err != nil {
				return supportBundleResult{}, err
			}
		}
		artifacts["probeData"] = filepath.Base(probePath)
	}

	manifestPath := filepath.Join(dir, "manifest.json")
	reportPath := filepath.Join(dir, "report.html")
	artifacts["report"] = filepath.Base(reportPath)
	manifest := supportBundleManifest("passed", backend, artifacts)
	if err := writeSupportJSON(manifestPath, manifest); err != nil {
		return supportBundleResult{}, err
	}
	report, err := loadProbeEvidenceReport(manifestPath, nil)
	if err != nil {
		return supportBundleResult{}, err
	}
	if !report.OK {
		manifest["status"] = "failed"
		if err := writeSupportJSON(manifestPath, manifest); err != nil {
			return supportBundleResult{}, err
		}
		report, err = loadProbeEvidenceReport(manifestPath, nil)
		if err != nil {
			return supportBundleResult{}, err
		}
	}
	if err := writeProbeEvidenceHTML(reportPath, report); err != nil {
		return supportBundleResult{}, err
	}
	return supportBundleResult{Dir: dir, ManifestPath: manifestPath, ReportPath: reportPath, Report: report}, nil
}

func supportRouteEvidence(ctx context.Context) (protocol.AgentRouteResult, error) {
	accounts, _, err := openAccountDeps()
	if err != nil {
		return protocol.AgentRouteResult{}, err
	}
	defer accounts.Close()
	return routeCLI(discovery.Discover(ctx, daemon.Registry()), accounts, routeCLIParams{
		AccountID: protocol.AccountAuto,
		Capabilities: protocol.AgentCapabilities{
			Usage:  true,
			Resume: true,
		},
	})
}

func supportBundleManifest(status, backend string, artifacts map[string]string) map[string]any {
	return map[string]any{
		"manifestVersion": 1,
		"status":          status,
		"stage":           "support-bundle",
		"backend":         backend,
		"daemonMode":      "external",
		"checkedAt":       time.Now().Unix(),
		"artifacts":       artifacts,
	}
}

func supportErrorArtifact(source string, err error) map[string]any {
	return map[string]any{
		"ok":     false,
		"source": source,
		"error":  err.Error(),
	}
}

func writeSupportJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeRawSupportJSON(path, data)
}

func writeRawSupportJSON(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if !json.Valid(data) {
		return fmt.Errorf("support artifact %s is not valid JSON", path)
	}
	return os.WriteFile(path, data, 0o600)
}
