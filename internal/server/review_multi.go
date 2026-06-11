package server

import (
	"context"
	"os"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/discovery"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func (s *Server) reviewMulti(ctx context.Context, client *wsClient, params protocol.TaskReviewMultiParams) (protocol.TaskReviewMultiResult, *protocol.Error) {
	cwd, perr := reviewCwd(params.Cwd)
	if perr != nil {
		return protocol.TaskReviewMultiResult{}, perr
	}
	if !validPermissionMode(params.PermissionMode) {
		return protocol.TaskReviewMultiResult{}, protocol.NewError(protocol.CodeInvalidParams, "unknown permissionMode %q", params.PermissionMode)
	}
	if params.PermissionMode == protocol.PermissionFull && isFilesystemRoot(cwd) {
		return protocol.TaskReviewMultiResult{}, protocol.NewError(protocol.CodeInvalidParams, "permissionMode %q is not allowed at filesystem root", protocol.PermissionFull)
	}
	agentIDs, perr := s.reviewAgentIDs(ctx, params.AgentIDs)
	if perr != nil {
		return protocol.TaskReviewMultiResult{}, perr
	}

	result := protocol.TaskReviewMultiResult{Reviews: []protocol.ReviewSession{}}
	for _, agentID := range agentIDs {
		sess, err := s.opts.Sessions.Create(ctx, agentID, adapter.SessionOpts{
			Cwd:            cwd,
			PermissionMode: params.PermissionMode,
			Model:          params.Model,
			Effort:         params.Effort,
		})
		if err != nil {
			return result, asProtocolError(err)
		}
		s.subscribe(ctx, client, sess, 0)
		if err := sess.Review(ctx, params.Target); err != nil {
			_ = s.opts.Sessions.Close(sess.ID)
			return result, asProtocolError(err)
		}
		result.Reviews = append(result.Reviews, protocol.ReviewSession{
			AgentID:   agentID,
			SessionID: sess.ID,
		})
	}
	return result, nil
}

func reviewCwd(cwd string) (string, *protocol.Error) {
	if cwd == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", protocol.NewError(protocol.CodeInternalError, "no cwd given and no home dir: %v", err)
		}
		cwd = home
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return "", protocol.NewError(protocol.CodeInvalidParams, "cwd %q is not a directory", cwd)
	}
	return cwd, nil
}

func (s *Server) reviewAgentIDs(ctx context.Context, requested []string) ([]string, *protocol.Error) {
	if len(requested) > 0 {
		out := make([]string, 0, len(requested))
		for _, id := range requested {
			if id == protocol.AgentAuto {
				routed, perr := s.routeAgent(ctx, protocol.AgentRouteParams{
					Capabilities: protocol.AgentCapabilities{Review: true},
				})
				if perr != nil {
					return nil, perr
				}
				id = routed.Agent.ID
			}
			if _, ok := s.opts.Registry.Get(id); !ok {
				return nil, protocol.NewError(protocol.CodeAgentNotFound, "unknown agent %q", id)
			}
			if !contains(out, id) {
				out = append(out, id)
			}
		}
		return out, nil
	}

	var out []string
	for _, info := range discovery.Discover(ctx, s.opts.Registry) {
		if info.Available && info.Capabilities.Review {
			out = append(out, info.ID)
		}
	}
	if len(out) == 0 {
		return nil, protocol.NewError(protocol.CodeAgentUnavailable, "no available review-capable agents")
	}
	return out, nil
}

func contains(xs []string, x string) bool {
	for _, item := range xs {
		if item == x {
			return true
		}
	}
	return false
}
