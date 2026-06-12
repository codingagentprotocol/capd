package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/daemon"
	"github.com/codingagentprotocol/capd/internal/discovery"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func newAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Inspect coding agent CLIs on this machine",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "Probe and list discovered agent CLIs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			infos := discovery.Discover(cmd.Context(), daemon.Registry())
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTATUS\tVERSION\tBIN")
			for _, a := range infos {
				status := "not installed"
				if a.Available {
					status = "available"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.ID, status, a.Version, a.Bin)
			}
			return w.Flush()
		},
	})
	routeCmd := &cobra.Command{
		Use:   "route",
		Short: "Preview local agent and account routing",
		RunE: func(cmd *cobra.Command, _ []string) error {
			params, err := routeCLIParamsFromFlags(cmd)
			if err != nil {
				return err
			}
			var accounts *account.Store
			if params.AccountID != "" {
				accounts, _, err = openAccountDeps()
				if err != nil {
					return err
				}
				defer accounts.Close()
			}
			result, err := routeCLI(discovery.Discover(cmd.Context(), daemon.Registry()), accounts, params)
			if err != nil {
				return err
			}
			if params.JSON {
				out, _ := json.MarshalIndent(result, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s", result.Agent.ID)
			if result.AccountID != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "\t%s", result.AccountID)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\t%s\n", result.Reason)
			return nil
		},
	}
	routeCmd.Flags().String("account", "", "imported account id, or auto for conservative Codex quota scoring")
	routeCmd.Flags().String("model", "", "model requirement; routes to agents with model support")
	routeCmd.Flags().String("effort", "", "effort requirement; routes to agents with effort support")
	routeCmd.Flags().StringSlice("capability", nil, "required capability name; repeat or comma-separate")
	routeCmd.Flags().StringSlice("prefer", nil, "preferred agent id order; repeat or comma-separate")
	routeCmd.Flags().Bool("require-fresh-quota", false, "fail unless --account auto is backed by fresh cached quota")
	routeCmd.Flags().Bool("json", false, "print route result as JSON")
	usageCmd := &cobra.Command{
		Use:   "usage <agent-id>",
		Short: "Account usage and rate limits for one agent (plan, used %, reset times)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			accountID, _ := cmd.Flags().GetString("account")
			a, ok := daemon.Registry().Get(args[0])
			if !ok {
				return fmt.Errorf("unknown agent %q", args[0])
			}
			up, ok := a.(adapter.UsageProvider)
			if !ok {
				return fmt.Errorf("agent %q does not report usage", args[0])
			}
			var usage map[string]any
			var err error
			if accountID != "" {
				accountUp, ok := a.(adapter.AccountUsageProvider)
				if !ok {
					return fmt.Errorf("agent %q does not report account-specific usage", args[0])
				}
				accounts, secrets, err := openAccountDeps()
				if err != nil {
					return err
				}
				defer accounts.Close()
				acc, err := resolveUsageAccount(accounts, accountID)
				if err != nil {
					return err
				}
				home, err := daemon.Home()
				if err != nil {
					return err
				}
				profile, err := codexauth.RuntimeProjector{
					Root:    filepath.Join(home, "runtimes"),
					Secrets: secrets,
				}.Project(cmd.Context(), acc)
				if err != nil {
					return err
				}
				usage, err = accountUp.UsageFor(cmd.Context(), adapter.SessionOpts{Env: profile.Env})
				if err == nil {
					if err := accounts.SaveQuota(account.QuotaFromUsage(acc.ID, usage)); err != nil {
						return err
					}
				}
			} else {
				usage, err = up.Usage(cmd.Context())
			}
			if err != nil {
				return err
			}
			out, _ := json.MarshalIndent(usage, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}
	usageCmd.Flags().String("account", "", "imported account id for account-specific usage (currently Codex)")
	cmd.AddCommand(routeCmd, usageCmd)
	return cmd
}

type routeCLIParams struct {
	AccountID    string
	Model        string
	Effort       string
	Capabilities protocol.AgentCapabilities
	Prefer       []string
	RequireFresh bool
	JSON         bool
}

var cliDefaultRoutePreference = []string{
	"codex",
	"claude-code",
	"opencode",
	"gemini",
	"cursor-agent",
}

func routeCLIParamsFromFlags(cmd *cobra.Command) (routeCLIParams, error) {
	accountID, _ := cmd.Flags().GetString("account")
	model, _ := cmd.Flags().GetString("model")
	effort, _ := cmd.Flags().GetString("effort")
	prefer, _ := cmd.Flags().GetStringSlice("prefer")
	capabilityNames, _ := cmd.Flags().GetStringSlice("capability")
	requireFresh, _ := cmd.Flags().GetBool("require-fresh-quota")
	jsonOut, _ := cmd.Flags().GetBool("json")
	required, err := agentCapabilitiesFromNames(capabilityNames)
	if err != nil {
		return routeCLIParams{}, err
	}
	if model != "" {
		required.Model = true
	}
	if effort != "" {
		required.Effort = true
	}
	return routeCLIParams{
		AccountID:    strings.TrimSpace(accountID),
		Model:        model,
		Effort:       effort,
		Capabilities: required,
		Prefer:       prefer,
		RequireFresh: requireFresh,
		JSON:         jsonOut,
	}, nil
}

func agentCapabilitiesFromNames(names []string) (protocol.AgentCapabilities, error) {
	var c protocol.AgentCapabilities
	for _, raw := range names {
		for _, part := range strings.Split(raw, ",") {
			name := strings.TrimSpace(part)
			if name == "" {
				continue
			}
			switch name {
			case "model":
				c.Model = true
			case "effort":
				c.Effort = true
			case "streaming":
				c.Streaming = true
			case "approvals":
				c.Approvals = true
			case "steer":
				c.Steer = true
			case "fork":
				c.Fork = true
			case "rollback":
				c.Rollback = true
			case "review":
				c.Review = true
			case "images":
				c.Images = true
			case "usage":
				c.Usage = true
			case "resume":
				c.Resume = true
			default:
				return protocol.AgentCapabilities{}, fmt.Errorf("unknown capability %q", name)
			}
		}
	}
	return c, nil
}

func routeCLI(infos []protocol.AgentInfo, accounts *account.Store, params routeCLIParams) (protocol.AgentRouteResult, error) {
	required := params.Capabilities
	prefer := params.Prefer
	if len(prefer) == 0 {
		prefer = cliDefaultRoutePreference
	}
	accountID := strings.TrimSpace(params.AccountID)
	selectedAccountID := ""
	accountReason := ""
	if accountID != "" {
		prefer = []string{codexauth.Provider}
		required.Usage = true
		required.Resume = true
		if accounts == nil {
			return protocol.AgentRouteResult{}, fmt.Errorf("account support is not configured")
		}
		if accountID == protocol.AccountAuto {
			acc, err := account.SelectQuotaRouteAccount(accounts, codexauth.Provider)
			if errors.Is(err, account.ErrUnknownAccount) {
				return protocol.AgentRouteResult{}, fmt.Errorf("no imported Codex accounts; run capd accounts codex import first")
			}
			if err != nil {
				return protocol.AgentRouteResult{}, err
			}
			selectedAccountID = acc.ID
			if params.RequireFresh {
				if q, err := accounts.LoadQuota(acc.ID); err != nil || !account.QuotaSnapshotFresh(q, time.Now()) {
					return protocol.AgentRouteResult{}, fmt.Errorf("auto route does not have fresh cached quota; run capd accounts codex smoke --quota --require-fresh-quota or refresh quota first")
				}
			}
			accountReason = autoRouteReason(accounts, acc)
		} else {
			acc, err := resolveUsageAccount(accounts, accountID)
			if err != nil {
				return protocol.AgentRouteResult{}, err
			}
			selectedAccountID = acc.ID
			accountReason = "explicit accountId"
		}
	}

	var best protocol.AgentInfo
	bestScore := -1
	for _, info := range infos {
		if accountID != "" && info.ID != codexauth.Provider {
			continue
		}
		if !info.Available || !hasCLICapabilities(info.Capabilities, required) {
			continue
		}
		score := routeCLIScore(info, prefer)
		if score > bestScore {
			best = info
			bestScore = score
		}
	}
	if bestScore < 0 {
		return protocol.AgentRouteResult{}, fmt.Errorf("no available agent satisfies requested capabilities")
	}
	reason := "matched capabilities" + routeCLICapabilitySuffix(required)
	if accountID != "" {
		reason += "; accountId requires codex account runtime"
		if accountReason != "" {
			reason += "; " + accountReason
		}
	}
	return protocol.AgentRouteResult{Agent: best, AccountID: selectedAccountID, Reason: reason}, nil
}

func autoRouteReason(accounts *account.Store, acc account.Account) string {
	if q, err := accounts.LoadQuota(acc.ID); err == nil && account.QuotaSnapshotFresh(q, time.Now()) {
		return fmt.Sprintf("auto account %s primary %.0f%%", acc.ID, q.PrimaryUsedPercent)
	}
	return fmt.Sprintf("auto account %s without fresh cached quota", acc.ID)
}

func hasCLICapabilities(got, want protocol.AgentCapabilities) bool {
	return (!want.Model || got.Model) &&
		(!want.Effort || got.Effort) &&
		(!want.Streaming || got.Streaming) &&
		(!want.Approvals || got.Approvals) &&
		(!want.Steer || got.Steer) &&
		(!want.Fork || got.Fork) &&
		(!want.Rollback || got.Rollback) &&
		(!want.Review || got.Review) &&
		(!want.Images || got.Images) &&
		(!want.Usage || got.Usage) &&
		(!want.Resume || got.Resume)
}

func routeCLIScore(info protocol.AgentInfo, prefer []string) int {
	score := countCLICapabilities(info.Capabilities)
	if idx := slices.Index(prefer, info.ID); idx >= 0 {
		score += 1000 - idx
	}
	return score
}

func countCLICapabilities(c protocol.AgentCapabilities) int {
	n := 0
	for _, enabled := range []bool{
		c.Model, c.Effort, c.Streaming, c.Approvals, c.Steer, c.Fork,
		c.Rollback, c.Review, c.Images, c.Usage, c.Resume,
	} {
		if enabled {
			n++
		}
	}
	return n
}

func routeCLICapabilitySuffix(required protocol.AgentCapabilities) string {
	var names []string
	if required.Model {
		names = append(names, "model")
	}
	if required.Effort {
		names = append(names, "effort")
	}
	if required.Streaming {
		names = append(names, "streaming")
	}
	if required.Approvals {
		names = append(names, "approvals")
	}
	if required.Steer {
		names = append(names, "steer")
	}
	if required.Fork {
		names = append(names, "fork")
	}
	if required.Rollback {
		names = append(names, "rollback")
	}
	if required.Review {
		names = append(names, "review")
	}
	if required.Images {
		names = append(names, "images")
	}
	if required.Usage {
		names = append(names, "usage")
	}
	if required.Resume {
		names = append(names, "resume")
	}
	if len(names) == 0 {
		return ""
	}
	return ": " + strings.Join(names, ", ")
}

func resolveUsageAccount(accounts *account.Store, accountID string) (account.Account, error) {
	if accountID == protocol.AccountAuto {
		acc, err := account.SelectQuotaRouteAccount(accounts, codexauth.Provider)
		if errors.Is(err, account.ErrUnknownAccount) {
			return account.Account{}, fmt.Errorf("no imported Codex accounts; run capd accounts codex import first")
		}
		return acc, err
	}
	acc, err := accounts.LoadAccount(accountID)
	if err != nil {
		return account.Account{}, err
	}
	if acc.Provider != codexauth.Provider {
		return account.Account{}, fmt.Errorf("account %q is not a Codex account", accountID)
	}
	return acc, nil
}
