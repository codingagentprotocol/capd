package server

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/discovery"
	"github.com/codingagentprotocol/capd/internal/security"
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
	if !client.initialized && req.Method != protocol.MethodInitialize {
		return nil, protocol.NewError(protocol.CodeInvalidRequest, "initialize must be the first request")
	}
	if !clientScopeAllows(client.auth.Scope, req.Method) {
		return nil, protocol.NewError(protocol.CodeUnauthorized, "token scope %q cannot call %s", client.auth.Scope, req.Method)
	}

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
		client.initialized = true
		return res, nil

	case protocol.MethodAgentsList:
		agents := discovery.Discover(ctx, s.opts.Registry)
		return protocol.AgentsListResult{Agents: agents}, nil

	case protocol.MethodAgentsRoute:
		var params protocol.AgentRouteParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		return s.routeAgent(ctx, params)

	case protocol.MethodAgentsUsage:
		var params protocol.AgentsUsageParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		params.AgentID = strings.TrimSpace(params.AgentID)
		params.AccountID = strings.TrimSpace(params.AccountID)
		a, ok := s.opts.Registry.Get(params.AgentID)
		if !ok {
			return nil, protocol.NewError(protocol.CodeAgentNotFound, "unknown agent %q", params.AgentID)
		}
		up, ok := a.(adapter.UsageProvider)
		if !ok {
			return nil, protocol.NewError(protocol.CodeMethodNotFound, "agent %q does not report usage", params.AgentID)
		}
		var usage map[string]any
		var err error
		effectiveAccountID := params.AccountID
		if params.AccountID != "" {
			if params.AccountID == protocol.AccountAuto {
				acc, _, perr := s.selectCodexAccountForRoute("")
				if perr != nil {
					return nil, perr
				}
				effectiveAccountID = acc.ID
			}
			env, perr := s.runtimeEnvForAccount(ctx, params.AgentID, effectiveAccountID)
			if perr != nil {
				return nil, perr
			}
			accountUp, ok := a.(adapter.AccountUsageProvider)
			if !ok {
				return nil, protocol.NewError(protocol.CodeMethodNotFound, "agent %q does not report account-specific usage", params.AgentID)
			}
			usage, err = accountUp.UsageFor(ctx, adapter.SessionOpts{Env: env})
		} else {
			usage, err = up.Usage(ctx)
		}
		if err != nil {
			return nil, protocol.NewError(protocol.CodeAgentUnavailable, "usage: %v", err)
		}
		if effectiveAccountID != "" {
			if perr := s.saveUsageQuota(ctx, effectiveAccountID, usage); perr != nil {
				return nil, perr
			}
		}
		return protocol.AgentsUsageResult{AgentID: params.AgentID, AccountID: effectiveAccountID, Usage: usage}, nil

	case protocol.MethodAccountsList:
		var params protocol.AccountsListParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		return s.listAccounts(params)

	case protocol.MethodAccountsImport:
		var params protocol.AccountsImportParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		return s.importAccount(ctx, params)

	case protocol.MethodAccountsCurrent:
		var params protocol.AccountsCurrentParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		return s.currentAccount(params)

	case protocol.MethodAccountsProject:
		var params protocol.AccountsProjectParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		return s.projectAccountRuntime(ctx, params)

	case protocol.MethodAccountsCheck:
		var params protocol.AccountsCheckParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		return s.checkAccounts(ctx, params)

	case protocol.MethodAccountsQuota:
		var params protocol.AccountsQuotaParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		return s.refreshAccountQuota(ctx, params)

	case protocol.MethodAccountsRemove:
		var params protocol.AccountsRemoveParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		return s.removeAccount(ctx, params)

	case protocol.MethodProfilesList:
		var params protocol.ProfilesListParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		return s.listProfiles(params)

	case protocol.MethodProfilesUpdate:
		var params protocol.ProfilesUpdateParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		return s.updateProfile(params)

	case protocol.MethodProfilesMembers:
		var params protocol.ProfilesMembersParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		return s.updateProfileMembers(params)

	case protocol.MethodSessionCreate:
		var params protocol.SessionCreateParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		params.AgentID = strings.TrimSpace(params.AgentID)
		params.AccountID = strings.TrimSpace(params.AccountID)
		params.Profile = strings.TrimSpace(params.Profile)
		if params.Cwd == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, protocol.NewError(protocol.CodeInternalError, "no cwd given and no home dir: %v", err)
			}
			params.Cwd = home
		}
		if info, err := os.Stat(params.Cwd); err != nil || !info.IsDir() {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "cwd %q is not a directory", params.Cwd)
		}
		if perr := s.policy.validateSessionCreate(params); perr != nil {
			return nil, perr
		}
		agentID := params.AgentID
		if agentID == protocol.AgentAuto {
			routed, perr := s.routeAgent(ctx, routeParamsForCreate(params))
			if perr != nil {
				return nil, perr
			}
			agentID = routed.Agent.ID
			if params.AccountID == protocol.AccountAuto {
				params.AccountID = routed.AccountID
			}
		} else if params.AccountID == protocol.AccountAuto {
			routed, perr := s.routeAgent(ctx, protocol.AgentRouteParams{
				AccountID:         protocol.AccountAuto,
				Profile:           params.Profile,
				Model:             params.Model,
				Effort:            params.Effort,
				Capabilities:      routeRequirements(routeParamsForCreate(params)),
				Prefer:            []string{agentID},
				RequireFreshQuota: params.RequireFreshQuota,
			})
			if perr != nil {
				return nil, perr
			}
			if routed.Agent.ID != agentID {
				return nil, protocol.NewError(protocol.CodeInvalidParams, "accountId auto is currently supported only for agent %q", codexAgentID)
			}
			params.AccountID = routed.AccountID
		}
		opts := adapter.SessionOpts{
			Cwd:            params.Cwd,
			Resume:         params.Resume,
			PermissionMode: params.PermissionMode,
			Model:          params.Model,
			Effort:         params.Effort,
		}
		if params.AccountID != "" {
			env, perr := s.runtimeEnvForAccount(ctx, agentID, params.AccountID)
			if perr != nil {
				return nil, perr
			}
			opts.Env = env
		}
		sess, err := s.opts.Sessions.Create(ctx, agentID, opts)
		if err != nil {
			s.metrics.recordAdapterStart(false)
			return nil, asProtocolError(err)
		}
		s.metrics.recordAdapterStart(true)
		if params.AccountID != "" && s.opts.Accounts != nil {
			if err := s.opts.Accounts.BindSessionAccount(sess.ID, params.AccountID); err != nil {
				return nil, protocol.NewError(protocol.CodeInternalError, "bind session account: %v", err)
			}
		}
		s.subscribe(ctx, client, sess, 0)
		return protocol.SessionCreateResult{SessionID: sess.ID, AccountID: params.AccountID}, nil

	case protocol.MethodSessionList:
		return s.listSessions()

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

	case protocol.MethodSessionHistory:
		var params protocol.SessionHistoryParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		limit := params.Limit
		if limit <= 0 {
			limit = 500
		}
		if limit > 5000 {
			limit = 5000
		}
		events, err := s.opts.Sessions.History(params.SessionID, params.FromSeq, limit)
		if err != nil {
			return nil, asProtocolError(err)
		}
		next := params.FromSeq
		if n := len(events); n > 0 {
			next = events[n-1].Seq + 1
		}
		if events == nil {
			events = []protocol.Event{}
		}
		return protocol.SessionHistoryResult{SessionID: params.SessionID, Events: events, NextSeq: next}, nil

	case protocol.MethodSessionFork:
		var params protocol.SessionForkParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		forked, err := s.opts.Sessions.Fork(ctx, params.SessionID)
		if err != nil {
			return nil, asProtocolError(err)
		}
		if s.opts.Accounts != nil {
			accountID, err := s.opts.Accounts.SessionAccount(params.SessionID)
			if err != nil {
				return nil, protocol.NewError(protocol.CodeInternalError, "load parent session account: %v", err)
			}
			if accountID != "" {
				if err := s.opts.Accounts.BindSessionAccount(forked.ID, accountID); err != nil {
					return nil, protocol.NewError(protocol.CodeInternalError, "bind forked session account: %v", err)
				}
			}
		}
		s.subscribe(ctx, client, forked, 0)
		return protocol.SessionForkResult{SessionID: forked.ID}, nil

	case protocol.MethodSessionRollback:
		var params protocol.SessionRollbackParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		if params.NumTurns < 1 {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "numTurns must be >= 1")
		}
		sess, err := s.opts.Sessions.Resolve(ctx, params.SessionID)
		if err != nil {
			return nil, asProtocolError(err)
		}
		if err := sess.Rollback(ctx, params.NumTurns); err != nil {
			return nil, asProtocolError(err)
		}
		return protocol.OKResult{OK: true}, nil

	case protocol.MethodTaskReview:
		var params protocol.TaskReviewParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		sess, err := s.opts.Sessions.Resolve(ctx, params.SessionID)
		if err != nil {
			return nil, asProtocolError(err)
		}
		if err := sess.Review(ctx, params.Target); err != nil {
			return nil, asProtocolError(err)
		}
		return protocol.OKResult{OK: true}, nil

	case protocol.MethodTaskReviewMulti:
		var params protocol.TaskReviewMultiParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, "%v", err)
		}
		return s.reviewMulti(ctx, client, params)

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
		if perr := s.policy.validateTaskSend(params); perr != nil {
			return nil, perr
		}
		sess, err := s.opts.Sessions.Resolve(ctx, params.SessionID)
		if err != nil {
			return nil, asProtocolError(err)
		}
		msg := adapter.Message{Prompt: params.Prompt, Images: params.Attachments}
		if err := sess.Send(ctx, msg); err != nil {
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
		if perr := s.policy.validateApprovalReply(params); perr != nil {
			return nil, perr
		}
		sess, err := s.opts.Sessions.Resolve(ctx, params.SessionID)
		if err != nil {
			return nil, asProtocolError(err)
		}
		if err := sess.Approve(ctx, params.ApprovalID, params.Decision); err != nil {
			s.recordApprovalAudit(params, "failed")
			return nil, asProtocolError(err)
		}
		s.recordApprovalAudit(params, "ok")
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

func clientScopeAllows(scope, method string) bool {
	switch scope {
	case "", security.TokenScopeFull:
		return true
	case security.TokenScopeProbeRead:
		switch method {
		case protocol.MethodInitialize,
			protocol.MethodAgentsList,
			protocol.MethodAgentsRoute,
			protocol.MethodAccountsList,
			protocol.MethodAccountsCheck:
			return true
		default:
			return false
		}
	case security.TokenScopeConsole, security.TokenScopeConsoleRead:
		switch method {
		case protocol.MethodInitialize,
			protocol.MethodAgentsList,
			protocol.MethodAgentsRoute,
			protocol.MethodAgentsUsage,
			protocol.MethodAccountsList,
			protocol.MethodAccountsImport,
			protocol.MethodAccountsCurrent,
			protocol.MethodAccountsProject,
			protocol.MethodAccountsCheck,
			protocol.MethodAccountsQuota,
			protocol.MethodAccountsRemove,
			protocol.MethodProfilesList,
			protocol.MethodProfilesUpdate,
			protocol.MethodProfilesMembers,
			protocol.MethodSessionList,
			protocol.MethodSessionAttach,
			protocol.MethodSessionHistory:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// subscribe wires a session's event stream to this client connection for the
// connection's lifetime. Returns the seq the live stream continues from.
func (s *Server) subscribe(ctx context.Context, client *wsClient, sess *session.Session, fromSeq uint64) uint64 {
	ch, nextSeq, cancel, overflow := sess.Subscribe(fromSeq)
	go func() {
		defer cancel()
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if !client.notify(protocol.MethodEvent, ev) {
					return
				}
			case <-overflow:
				client.notify(protocol.MethodEvent, protocol.Event{
					SessionID: sess.ID,
					Seq:       nextSeq,
					Type:      protocol.EventError,
					Data: map[string]any{
						"message":  "event stream overflow; reconnect with session/attach from the last received seq",
						"overflow": true,
					},
				})
				client.cancel()
				return
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
