package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// wsClient is one connected client. All writes go through the out channel so
// responses and streamed event notifications never interleave mid-frame.
type wsClient struct {
	conn        *websocket.Conn
	out         chan []byte
	cancel      context.CancelFunc
	initialized bool
}

// enqueue never blocks: a client that stops reading loses messages rather
// than stalling a session pump.
func (c *wsClient) enqueue(data []byte) bool {
	select {
	case c.out <- data:
		return true
	default:
		if c.cancel != nil {
			c.cancel()
		}
		return false
	}
}

func (c *wsClient) notify(method string, params any) bool {
	n, err := protocol.NewNotification(method, params)
	if err != nil {
		return false
	}
	data, err := json.Marshal(n)
	if err != nil {
		return false
	}
	return c.enqueue(data)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Localhost pages are always allowed; anything else must be
		// explicitly configured (CAPD_ORIGINS / --origins), never
		// defaulted open.
		OriginPatterns: append([]string{"localhost:*", "127.0.0.1:*", "[[]::1]:*"}, s.opts.Origins...),
	})
	if err != nil {
		s.log.Warn("ws accept failed", "err", err)
		return
	}
	defer conn.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	client := &wsClient{conn: conn, out: make(chan []byte, 512), cancel: cancel}
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
	go func() { // heartbeat: reap connections whose peer is gone
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pingCtx)
				pingCancel()
				if err != nil {
					s.log.Info("client heartbeat failed, dropping", "remote", r.RemoteAddr)
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
