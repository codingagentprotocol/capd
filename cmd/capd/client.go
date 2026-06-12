package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/coder/websocket"

	"github.com/codingagentprotocol/capd/internal/config"
	"github.com/codingagentprotocol/capd/internal/daemon"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func daemonWSURL(cfg config.Config) string {
	u := url.URL{
		Scheme: "ws",
		Host:   daemonAddr(cfg),
		Path:   "/ws",
	}
	return u.String()
}

func daemonDialOptions(token string) *websocket.DialOptions {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	return &websocket.DialOptions{HTTPHeader: h}
}

func consoleURL(cfg config.Config, token string) string {
	return localPageURL(cfg, token, "/console/")
}

func consoleURLWithSecretBackend(cfg config.Config, token, secretBackend string) string {
	return localPageURLWithParams(cfg, token, "/console/", map[string]string{"requireSecretBackend": secretBackend})
}

func probeURL(cfg config.Config, token string) string {
	return localPageURL(cfg, token, "/probe/")
}

func probeURLWithSecretBackend(cfg config.Config, token, secretBackend string) string {
	return localPageURLWithParams(cfg, token, "/probe/", map[string]string{"requireSecretBackend": secretBackend})
}

func localPageURL(cfg config.Config, token, path string) string {
	return localPageURLWithParams(cfg, token, path, nil)
}

func localPageURLWithParams(cfg config.Config, token, path string, params map[string]string) string {
	u := url.URL{
		Scheme: "http",
		Host:   daemonAddr(cfg),
		Path:   path,
	}
	q := u.Query()
	q.Set("token", token)
	for key, value := range params {
		if value != "" {
			q.Set(key, value)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func daemonAddr(cfg config.Config) string {
	return net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
}

func daemonConnectError(cfg config.Config, token string, err error) error {
	return fmt.Errorf("connect to capd at %s (is 'capd start' running?): %s", daemonAddr(cfg), redactDaemonToken(err.Error(), token))
}

func redactDaemonToken(text, token string) string {
	if token == "" {
		return text
	}
	for _, value := range []string{token, url.QueryEscape(token)} {
		if value != "" {
			text = strings.ReplaceAll(text, value, "<redacted>")
		}
	}
	return text
}

func daemonRPCCall(ctx context.Context, clientName, method string, params any) (json.RawMessage, error) {
	cfg := config.Load()
	home, err := daemon.Home()
	if err != nil {
		return nil, err
	}
	tokenBytes, err := os.ReadFile(filepath.Join(home, "token"))
	if err != nil {
		return nil, fmt.Errorf("no daemon token (is capd started?): %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	conn, _, err := websocket.Dial(ctx, daemonWSURL(cfg), daemonDialOptions(token))
	if err != nil {
		return nil, daemonConnectError(cfg, token, err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(32 * 1024 * 1024)

	nextID := 0
	call := func(method string, params any) (json.RawMessage, error) {
		nextID++
		payload, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		id, _ := json.Marshal(nextID)
		req, _ := json.Marshal(protocol.Request{
			JSONRPC: protocol.JSONRPCVersion,
			ID:      (*json.RawMessage)(&id),
			Method:  method,
			Params:  payload,
		})
		if err := conn.Write(ctx, websocket.MessageText, req); err != nil {
			return nil, err
		}
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return nil, err
			}
			var probe struct {
				ID     *int   `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(data, &probe) != nil || probe.Method == protocol.MethodEvent {
				continue
			}
			if probe.ID == nil || *probe.ID != nextID {
				continue
			}
			var resp protocol.Response
			if err := json.Unmarshal(data, &resp); err != nil {
				return nil, err
			}
			if resp.Error != nil {
				return nil, resp.Error
			}
			return resp.Result, nil
		}
	}
	if _, err := call(protocol.MethodInitialize, protocol.InitializeParams{
		ProtocolVersion: protocol.Version,
		Client:          protocol.ClientInfo{Name: clientName, Version: daemon.Version},
	}); err != nil {
		return nil, err
	}
	return call(method, params)
}
