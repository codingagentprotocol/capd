package server

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/codingagentprotocol/capd/internal/discovery"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

var defaultRoutePreference = []string{
	"codex",
	"claude-code",
	"opencode",
	"gemini",
	"cursor-agent",
}

func routeParamsForCreate(params protocol.SessionCreateParams) protocol.AgentRouteParams {
	required := protocol.AgentCapabilities{}
	if params.Model != "" {
		required.Model = true
	}
	if params.Effort != "" {
		required.Effort = true
	}
	return protocol.AgentRouteParams{
		Model:        params.Model,
		Effort:       params.Effort,
		Capabilities: required,
	}
}

func (s *Server) routeAgent(ctx context.Context, params protocol.AgentRouteParams) (protocol.AgentRouteResult, *protocol.Error) {
	required := routeRequirements(params)
	prefer := params.Prefer
	if len(prefer) == 0 {
		prefer = defaultRoutePreference
	}

	var best protocol.AgentInfo
	bestScore := -1
	for _, agent := range discovery.Discover(ctx, s.opts.Registry) {
		if !agent.Available || !hasCapabilities(agent.Capabilities, required) {
			continue
		}
		score := routeScore(agent, prefer)
		if score > bestScore {
			best = agent
			bestScore = score
		}
	}
	if bestScore < 0 {
		return protocol.AgentRouteResult{}, protocol.NewError(
			protocol.CodeAgentUnavailable, "no available agent satisfies requested capabilities")
	}
	return protocol.AgentRouteResult{
		Agent:  best,
		Reason: fmt.Sprintf("matched capabilities%s", routeReasonSuffix(required)),
	}, nil
}

func routeRequirements(params protocol.AgentRouteParams) protocol.AgentCapabilities {
	required := params.Capabilities
	if params.Model != "" {
		required.Model = true
	}
	if params.Effort != "" {
		required.Effort = true
	}
	if len(params.Attachments) > 0 {
		required.Images = true
	}
	return required
}

func hasCapabilities(got, want protocol.AgentCapabilities) bool {
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

func routeScore(agent protocol.AgentInfo, prefer []string) int {
	score := countCapabilities(agent.Capabilities)
	if idx := slices.Index(prefer, agent.ID); idx >= 0 {
		score += 1000 - idx
	}
	return score
}

func countCapabilities(c protocol.AgentCapabilities) int {
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

func routeReasonSuffix(required protocol.AgentCapabilities) string {
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
