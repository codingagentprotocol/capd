package server

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/codingagentprotocol/capd/internal/security"
)

// authorized checks the daemon token on a handshake request. Browsers cannot
// set arbitrary headers on WebSocket upgrades, so the query parameter form
// (?token=...) is accepted alongside Authorization: Bearer.
func (s *Server) authorized(r *http.Request) bool {
	tok := r.URL.Query().Get("token")
	if tok == "" {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			tok = strings.TrimPrefix(h, "Bearer ")
		}
	}
	if tok == "" {
		return false
	}
	if err := security.ValidateHeaderValue(tok); err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(s.opts.Token)) == 1
}
