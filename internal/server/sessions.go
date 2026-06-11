package server

import (
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func (s *Server) listSessions() (protocol.SessionListResult, *protocol.Error) {
	sessions := s.opts.Sessions.List(100)
	if s.opts.Accounts == nil {
		return protocol.SessionListResult{Sessions: sessions}, nil
	}
	for i := range sessions {
		accountID, err := s.opts.Accounts.SessionAccount(sessions[i].SessionID)
		if err != nil {
			return protocol.SessionListResult{}, protocol.NewError(protocol.CodeInternalError, "load session account: %v", err)
		}
		sessions[i].AccountID = accountID
	}
	return protocol.SessionListResult{Sessions: sessions}, nil
}
