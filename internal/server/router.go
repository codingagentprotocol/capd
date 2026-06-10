package server

import (
	"context"
	"encoding/json"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/discovery"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// dispatch routes one request to its handler and builds the response.
func (s *Server) dispatch(ctx context.Context, req *protocol.Request) *protocol.Response {
	result, perr := s.handle(ctx, req)
	if perr != nil {
		return protocol.NewErrorResponse(req.ID, perr)
	}
	resp, err := protocol.NewResponse(req.ID, result)
	if err != nil {
		return protocol.NewErrorResponse(req.ID,
			protocol.NewError(protocol.CodeInternalError, "marshal result: %v", err))
	}
	return resp
}

func (s *Server) handle(ctx context.Context, req *protocol.Request) (any, *protocol.Error) {
	switch req.Method {
	case protocol.MethodInitialize:
		var params protocol.InitializeParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		if params.ProtocolVersion != protocol.Version {
			return nil, protocol.NewError(protocol.CodeVersionUnsupported,
				"client speaks %q, daemon speaks %q", params.ProtocolVersion, protocol.Version)
		}
		res := protocol.InitializeResult{ProtocolVersion: protocol.Version}
		res.Daemon.Name = "capd"
		res.Daemon.Version = s.opts.Version
		return res, nil

	case protocol.MethodAgentsList:
		agents := discovery.Discover(ctx, s.opts.Registry)
		return protocol.AgentsListResult{Agents: agents}, nil

	case protocol.MethodSessionCreate:
		var params protocol.SessionCreateParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		sess, err := s.opts.Sessions.Create(ctx, params.AgentID, adapter.SessionOpts{
			Cwd:    params.Cwd,
			Resume: params.Resume,
		})
		if err != nil {
			if perr, ok := err.(*protocol.Error); ok {
				return nil, perr
			}
			return nil, protocol.NewError(protocol.CodeInternalError, "%v", err)
		}
		return protocol.SessionCreateResult{SessionID: sess.ID}, nil

	default:
		return nil, protocol.NewError(protocol.CodeMethodNotFound, "unknown method %q", req.Method)
	}
}
