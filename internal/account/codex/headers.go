package codex

import (
	"fmt"
	"net/http"

	"github.com/codingagentprotocol/capd/internal/security"
)

const (
	DefaultUserAgent  = "capd-codex"
	DefaultOriginator = "codex-tui"

	HeaderChatGPTAccountID = "ChatGPT-Account-Id"
	HeaderOpenAIBeta       = "OpenAI-Beta"
	HeaderOriginator       = "Originator"
	HeaderRequestID        = "X-Client-Request-Id"
	HeaderTurnMetadata     = "X-Codex-Turn-Metadata"
	HeaderVersion          = "Version"
)

type AuthMode string

const (
	AuthModeOAuth  AuthMode = "oauth"
	AuthModeAPIKey AuthMode = "api_key"
)

type Identity struct {
	AccountID string
	AuthMode  AuthMode
	UserAgent string
}

type RequestContext string

const (
	RequestQuota        RequestContext = "quota"
	RequestSubscription RequestContext = "subscription"
	RequestResponses    RequestContext = "responses"
	RequestWebSocket    RequestContext = "websocket"
)

type Options struct {
	RequestID    string
	Version      string
	TurnMetadata string
}

// BuildHeaders is the single place that turns Codex account identity into
// upstream HTTP headers. Callers must pass the token explicitly and must never
// log the returned header without security.RedactHeaders.
func BuildHeaders(ctx RequestContext, identity Identity, accessToken string, opts Options) (http.Header, error) {
	if accessToken == "" {
		return nil, fmt.Errorf("access token is required")
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+accessToken)
	h.Set("User-Agent", userAgent(identity))

	switch ctx {
	case RequestQuota:
		h.Set("Accept", "application/json")
		h.Set("Referer", "https://chatgpt.com/")
		setOAuthAccountHeaders(h, identity)
	case RequestSubscription:
		h.Set("Accept", "application/json")
		h.Set("Referer", "https://chatgpt.com/")
		h.Set("x-openai-target-path", "/backend-api/subscriptions")
		h.Set("x-openai-target-route", "GET /backend-api/subscriptions")
		setOAuthAccountHeaders(h, identity)
	case RequestResponses:
		h.Set("Content-Type", "application/json")
		h.Set("Accept", "text/event-stream")
		h.Set("Connection", "Keep-Alive")
		setCodexClientHeaders(h, identity, opts)
	case RequestWebSocket:
		h.Set(HeaderOpenAIBeta, "responses_websockets=2026-02-06")
		setCodexClientHeaders(h, identity, opts)
	default:
		return nil, fmt.Errorf("unknown header context %q", ctx)
	}

	if err := validateBuiltHeaders(h); err != nil {
		return nil, err
	}
	return h, nil
}

func userAgent(identity Identity) string {
	if identity.UserAgent != "" {
		return identity.UserAgent
	}
	return DefaultUserAgent
}

func setOAuthAccountHeaders(h http.Header, identity Identity) {
	if identity.AuthMode != AuthModeOAuth {
		return
	}
	if identity.AccountID != "" {
		h.Set(HeaderChatGPTAccountID, identity.AccountID)
	}
}

func setCodexClientHeaders(h http.Header, identity Identity, opts Options) {
	if identity.AuthMode == AuthModeOAuth {
		h.Set(HeaderOriginator, DefaultOriginator)
		if identity.AccountID != "" {
			h.Set(HeaderChatGPTAccountID, identity.AccountID)
		}
	}
	if opts.RequestID != "" {
		h.Set(HeaderRequestID, opts.RequestID)
	}
	if opts.Version != "" {
		h.Set(HeaderVersion, opts.Version)
	}
	if opts.TurnMetadata != "" {
		h.Set(HeaderTurnMetadata, opts.TurnMetadata)
	}
}

func validateBuiltHeaders(h http.Header) error {
	for name, values := range h {
		for _, value := range values {
			if err := security.ValidateHeaderValue(value); err != nil {
				return fmt.Errorf("header %q: %w", name, err)
			}
		}
	}
	return nil
}
