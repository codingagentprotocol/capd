package security

import (
	"net/http"
	"testing"
)

func TestValidateHeaderRejectsForbiddenClientHeaders(t *testing.T) {
	for _, name := range []string{
		"Authorization",
		"authorization",
		"Cookie",
		"X-API-Key",
		"OpenAI-Api-Key",
		"Proxy-Authorization",
	} {
		if err := ValidateHeader(name, "value"); err == nil {
			t.Fatalf("ValidateHeader(%q) succeeded, want rejection", name)
		}
	}
}

func TestValidateHeaderRejectsInjection(t *testing.T) {
	if err := ValidateHeader("X-Client-Request-Id", "ok\r\nAuthorization: Bearer token"); err == nil {
		t.Fatal("ValidateHeader accepted CRLF injection")
	}
}

func TestValidateHeadersRejectsForbiddenHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer secret")
	if err := ValidateHeaders(h); err == nil {
		t.Fatal("ValidateHeaders accepted Authorization")
	}
}

func TestRedactHeadersMasksSecretsAndShortensAccount(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer secret")
	h.Set("ChatGPT-Account-Id", "acct_1234567890abcdef")
	h.Set("User-Agent", "capd-test")

	got := RedactHeaders(h)
	if got["Authorization"][0] != "<redacted>" {
		t.Fatalf("Authorization was not redacted: %+v", got)
	}
	if got["Chatgpt-Account-Id"][0] != "acct...cdef" {
		t.Fatalf("account id was not shortened: %+v", got)
	}
	if got["User-Agent"][0] != "capd-test" {
		t.Fatalf("unexpected User-Agent: %+v", got)
	}
}
