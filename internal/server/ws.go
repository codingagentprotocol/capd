package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// wsClient is one connected client. All writes go through the out channel so
// responses and streamed event notifications never interleave mid-frame.
type wsClient struct {
	conn *websocket.Conn
	out  chan []byte
}

// enqueue never blocks: a client that stops reading loses messages rather
// than stalling a session pump.
func (c *wsClient) enqueue(data []byte) {
	select {
	case c.out <- data:
	default:
	}
}

func (c *wsClient) notify(method string, params any) {
	n, err := protocol.NewNotification(method, params)
	if err != nil {
		return
	}
	data, err := json.Marshal(n)
	if err != nil {
		return
	}
	c.enqueue(data)
}

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

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	client := &wsClient{conn: conn, out: make(chan []byte, 512)}
	go func() { // writer loop
		for {
			select {
			case data := <-client.out:
				if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	s.log.Info("client connected", "remote", r.RemoteAddr)
	if err := s.serveConn(ctx, client); err != nil {
		s.log.Info("client disconnected", "remote", r.RemoteAddr, "reason", err)
	}
}

// serveConn reads JSON-RPC requests until the connection drops.
func (s *Server) serveConn(ctx context.Context, client *wsClient) error {
	for {
		_, data, err := client.conn.Read(ctx)
		if err != nil {
			return err
		}

		var req protocol.Request
		if err := json.Unmarshal(data, &req); err != nil {
			s.reply(client, protocol.NewErrorResponse(nil,
				protocol.NewError(protocol.CodeParseError, "invalid JSON: %v", err)))
			continue
		}

		resp := s.dispatch(ctx, client, &req)
		if req.IsNotification() || resp == nil {
			continue
		}
		s.reply(client, resp)
	}
}

func (s *Server) reply(client *wsClient, resp *protocol.Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		s.log.Error("marshal response", "err", err)
		return
	}
	client.enqueue(data)
}
