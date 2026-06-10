package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Local-only daemon: web clients are expected to run on localhost
		// (e.g. the inspector). Remote web origins must be explicitly
		// configured in a future config option, never defaulted open.
		OriginPatterns: []string{"localhost:*", "127.0.0.1:*"},
	})
	if err != nil {
		s.log.Warn("ws accept failed", "err", err)
		return
	}
	defer conn.CloseNow()

	s.log.Info("client connected", "remote", r.RemoteAddr)
	if err := s.serveConn(r.Context(), conn); err != nil {
		s.log.Info("client disconnected", "remote", r.RemoteAddr, "reason", err)
	}
}

// serveConn reads JSON-RPC requests until the connection drops.
func (s *Server) serveConn(ctx context.Context, conn *websocket.Conn) error {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}

		var req protocol.Request
		if err := json.Unmarshal(data, &req); err != nil {
			s.reply(ctx, conn, protocol.NewErrorResponse(nil,
				protocol.NewError(protocol.CodeParseError, "invalid JSON: %v", err)))
			continue
		}

		resp := s.dispatch(ctx, &req)
		if req.IsNotification() || resp == nil {
			continue
		}
		s.reply(ctx, conn, resp)
	}
}

func (s *Server) reply(ctx context.Context, conn *websocket.Conn, resp *protocol.Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		s.log.Error("marshal response", "err", err)
		return
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		s.log.Warn("write response", "err", err)
	}
}
