package server

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/codingagentprotocol/capd/internal/security"
)

const webSocketAuthSubprotocolPrefix = "capd.auth."

// authorized checks the daemon token on a handshake request. Browser clients
// prefer Sec-WebSocket-Protocol because they cannot set Authorization during
// WebSocket upgrades; query tokens remain supported for older local clients.
func (s *Server) authorized(r *http.Request) (bool, string) {
	tok := r.URL.Query().Get("token")
	if tok == "" {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			tok = strings.TrimPrefix(h, "Bearer ")
		}
	}
	subprotocol := ""
	if tok == "" {
		tok, subprotocol = webSocketTokenFromSubprotocols(r.Header.Values("Sec-WebSocket-Protocol"))
	}
	if tok == "" {
		return false, ""
	}
	if err := security.ValidateHeaderValue(tok); err != nil {
		return false, ""
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(s.opts.Token)) == 1, subprotocol
}

func webSocketAuthSubprotocol(token string) string {
	return webSocketAuthSubprotocolPrefix + base64.RawURLEncoding.EncodeToString([]byte(token))
}

func webSocketTokenFromSubprotocols(values []string) (string, string) {
	for _, value := range values {
		for _, candidate := range strings.Split(value, ",") {
			protocol := strings.TrimSpace(candidate)
			if !strings.HasPrefix(protocol, webSocketAuthSubprotocolPrefix) {
				continue
			}
			raw := strings.TrimPrefix(protocol, webSocketAuthSubprotocolPrefix)
			token, err := base64.RawURLEncoding.DecodeString(raw)
			if err != nil {
				return "", ""
			}
			return string(token), protocol
		}
	}
	return "", ""
}
