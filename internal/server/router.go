package server

import (
	"context"
	"encoding/json"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/discovery"
	"github.com/codingagentprotocol/capd/internal/session"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// dispatch routes one request to its handler and builds the response.
func (s *Server) dispatch(ctx context.Context, client *wsClient, req *protocol.Request) *protocol.Response {
	result, perr := s.handle(ctx, client, req)
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

func (s *Server) handle(ctx context.Context, client *wsClient, req *protocol.Request) (any, *protocol.Error) {
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

	case protocol.MethodAgentsUsage:
		var params protocol.AgentsUsageParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		a, ok := s.opts.Registry.Get(params.AgentID)
		if !ok {
			return nil, protocol.NewError(protocol.CodeAgentNotFound, "unknown agent %q", params.AgentID)
		}
		up, ok := a.(adapter.UsageProvider)
		if !ok {
			return nil, protocol.NewError(protocol.CodeMethodNotFound, "agent %q does not report usage", params.AgentID)
		}
		usage, err := up.Usage(ctx)
		if err != nil {
			return nil, protocol.NewError(protocol.CodeAgentUnavailable, "usage: %v", err)
		}
		return protocol.AgentsUsageResult{AgentID: params.AgentID, Usage: usage}, nil

	case protocol.MethodSessionCreate:
		var params protocol.SessionCreateParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		sess, err := s.opts.Sessions.Create(ctx, params.AgentID, adapter.SessionOpts{
			Cwd:            params.Cwd,
			Resume:         params.Resume,
			PermissionMode: params.PermissionMode,
		})
		if err != nil {
			return nil, asProtocolError(err)
		}
		s.subscribe(ctx, client, sess, 0)
		return protocol.SessionCreateResult{SessionID: sess.ID}, nil

	case protocol.MethodSessionAttach:
		var params protocol.SessionAttachParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		sess, err := s.opts.Sessions.Resolve(ctx, params.SessionID)
		if err != nil {
			return nil, asProtocolError(err)
		}
		nextSeq := s.subscribe(ctx, client, sess, params.FromSeq)
		return protocol.SessionAttachResult{SessionID: sess.ID, NextSeq: nextSeq}, nil

	case protocol.MethodSessionClose:
		var params protocol.SessionCloseParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		if err := s.opts.Sessions.Close(params.SessionID); err != nil {
			return nil, asProtocolError(err)
		}
		return protocol.OKResult{OK: true}, nil

	case protocol.MethodTaskSend:
		var params protocol.TaskSendParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		sess, err := s.opts.Sessions.Resolve(ctx, params.SessionID)
		if err != nil {
			return nil, asProtocolError(err)
		}
		if err := sess.Send(ctx, params.Prompt); err != nil {
			return nil, protocol.NewError(protocol.CodeInternalError, "%v", err)
		}
		return protocol.OKResult{OK: true}, nil

	case protocol.MethodTaskSteer:
		var params protocol.TaskSteerParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		sess, err := s.opts.Sessions.Resolve(ctx, params.SessionID)
		if err != nil {
			return nil, asProtocolError(err)
		}
		if err := sess.Steer(ctx, params.Prompt); err != nil {
			return nil, asProtocolError(err)
		}
		return protocol.OKResult{OK: true}, nil

	case protocol.MethodApprovalReply:
		var params protocol.ApprovalReplyParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		sess, err := s.opts.Sessions.Resolve(ctx, params.SessionID)
		if err != nil {
			return nil, asProtocolError(err)
		}
		if err := sess.Approve(ctx, params.ApprovalID, params.Decision); err != nil {
			return nil, asProtocolError(err)
		}
		return protocol.OKResult{OK: true}, nil

	case protocol.MethodTaskCancel:
		var params protocol.TaskCancelParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		sess, err := s.opts.Sessions.Resolve(ctx, params.SessionID)
		if err != nil {
			return nil, asProtocolError(err)
		}
		sess.Cancel()
		return protocol.OKResult{OK: true}, nil

	default:
		return nil, protocol.NewError(protocol.CodeMethodNotFound, "unknown method %q", req.Method)
	}
}

// subscribe wires a session's event stream to this client connection for the
// connection's lifetime. Returns the seq the live stream continues from.
func (s *Server) subscribe(ctx context.Context, client *wsClient, sess *session.Session, fromSeq uint64) uint64 {
	ch, nextSeq, cancel := sess.Subscribe(fromSeq)
	go func() {
		defer cancel()
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return
				}
				client.notify(protocol.MethodEvent, ev)
			case <-ctx.Done():
				return
			}
		}
	}()
	return nextSeq
}

func asProtocolError(err error) *protocol.Error {
	if perr, ok := err.(*protocol.Error); ok {
		return perr
	}
	return protocol.NewError(protocol.CodeInternalError, "%v", err)
}
