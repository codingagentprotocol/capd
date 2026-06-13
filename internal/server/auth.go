package server

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/codingagentprotocol/capd/internal/security"
)

const webSocketAuthSubprotocolPrefix = "capd.auth."

type authInfo struct {
	Scope string
}

// authorized checks the daemon token on a handshake request. Browser clients
// prefer Sec-WebSocket-Protocol because they cannot set Authorization during
// WebSocket upgrades; query tokens remain supported for older local clients.
func (s *Server) authorized(r *http.Request) (authInfo, bool, string) {
	tok := r.URL.Query().Get("token")
	if tok == "" {
		tok = bearerToken(r)
	}
	subprotocol := ""
	if tok == "" {
		tok, subprotocol = webSocketTokenFromSubprotocols(r.Header.Values("Sec-WebSocket-Protocol"))
	}
	if tok == "" {
		return authInfo{}, false, ""
	}
	if err := security.ValidateHeaderValue(tok); err != nil {
		return authInfo{}, false, ""
	}
	if scope, ok := security.VerifyScopedToken(s.opts.Token, tok, time.Now()); ok {
		return authInfo{Scope: scope}, true, subprotocol
	}
	return authInfo{}, false, ""
}

func (s *Server) authorizedBearer(r *http.Request) bool {
	return s.authorizedBearerFor(r, security.TokenScopeFull)
}

func (s *Server) authorizedBearerFor(r *http.Request, allowedScopes ...string) bool {
	tok := bearerToken(r)
	if tok == "" {
		return false
	}
	if err := security.ValidateHeaderValue(tok); err != nil {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(tok), []byte(s.opts.Token)) == 1 {
		return true
	}
	scope, ok := security.VerifyScopedToken(s.opts.Token, tok, time.Now())
	if !ok {
		return false
	}
	for _, allowed := range allowedScopes {
		if scope == allowed {
			return true
		}
	}
	return false
}

func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
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
