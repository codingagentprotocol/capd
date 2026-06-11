package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/codingagentprotocol/capd/internal/account/codexauth"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

const codexAgentID = "codex"

func (s *Server) runtimeEnvForAccount(ctx context.Context, agentID, accountID string) ([]string, *protocol.Error) {
	if agentID != codexAgentID {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "accountId is currently supported only for agent %q", codexAgentID)
	}
	if s.opts.Accounts == nil || s.opts.Secrets == nil || s.opts.RuntimeRoot == "" {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "account support is not configured")
	}
	if strings.TrimSpace(accountID) == "" {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "accountId is required")
	}
	acc, err := s.opts.Accounts.LoadAccount(accountID)
	if err != nil {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "unknown accountId %q", accountID)
	}
	if acc.Provider != codexauth.Provider {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "accountId %q is not a Codex account", accountID)
	}
	profile, err := codexauth.RuntimeProjector{
		Root:    s.opts.RuntimeRoot,
		Secrets: s.opts.Secrets,
	}.Project(ctx, acc)
	if err != nil {
		return nil, protocol.NewError(protocol.CodeInternalError, "project account runtime: %v", err)
	}
	if len(profile.Env) == 0 {
		return nil, protocol.NewError(protocol.CodeInternalError, "%v", fmt.Errorf("empty runtime environment for account %q", accountID))
	}
	return profile.Env, nil
}
