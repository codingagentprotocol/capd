package codex

import (
	"strings"
	"testing"

	"github.com/codingagentprotocol/capd/internal/security"
)

func TestBuildQuotaHeadersOAuth(t *testing.T) {
	h, err := BuildHeaders(RequestQuota, Identity{
		AccountID: "acct_1234567890abcdef",
		AuthMode:  AuthModeOAuth,
	}, "tok_secret", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Get("Authorization"); got != "Bearer tok_secret" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := h.Get(HeaderChatGPTAccountID); got != "acct_1234567890abcdef" {
		t.Fatalf("account id = %q", got)
	}
	if got := h.Get("Referer"); got != "https://chatgpt.com/" {
		t.Fatalf("Referer = %q", got)
	}
	if got := security.RedactHeaders(h)["Authorization"][0]; got != "<redacted>" {
		t.Fatalf("Authorization not redacted: %q", got)
	}
}

func TestBuildResponsesHeadersAPIKeyDoesNotSetOAuthHeaders(t *testing.T) {
	h, err := BuildHeaders(RequestResponses, Identity{
		AccountID: "acct_should_not_leak",
		AuthMode:  AuthModeAPIKey,
	}, "tok_secret", Options{RequestID: "req_123"})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Get(HeaderOriginator); got != "" {
		t.Fatalf("Originator = %q, want empty", got)
	}
	if got := h.Get(HeaderChatGPTAccountID); got != "" {
		t.Fatalf("ChatGPT account id = %q, want empty", got)
	}
	if got := h.Get(HeaderRequestID); got != "req_123" {
		t.Fatalf("request id = %q", got)
	}
	if got := h.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q", got)
	}
}

func TestBuildWebSocketHeadersSetsBetaOnlyForWebSocket(t *testing.T) {
	ws, err := BuildHeaders(RequestWebSocket, Identity{AuthMode: AuthModeOAuth}, "tok_secret", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := ws.Get(HeaderOpenAIBeta); got != "responses_websockets=2026-02-06" {
		t.Fatalf("OpenAI-Beta = %q", got)
	}

	responses, err := BuildHeaders(RequestResponses, Identity{AuthMode: AuthModeOAuth}, "tok_secret", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := responses.Get(HeaderOpenAIBeta); got != "" {
		t.Fatalf("responses OpenAI-Beta = %q, want empty", got)
	}
}

func TestBuildHeadersRejectsInjection(t *testing.T) {
	_, err := BuildHeaders(RequestResponses, Identity{
		AuthMode:  AuthModeOAuth,
		UserAgent: "good\r\nAuthorization: Bearer injected",
	}, "tok_secret", Options{})
	if err == nil || !strings.Contains(err.Error(), "newline") {
		t.Fatalf("err = %v, want newline rejection", err)
	}
}

func TestBuildHeadersRequiresToken(t *testing.T) {
	if _, err := BuildHeaders(RequestQuota, Identity{AuthMode: AuthModeOAuth}, "", Options{}); err == nil {
		t.Fatal("BuildHeaders accepted empty access token")
	}
}
