package codexquota

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codingagentprotocol/capd/internal/account/secret"
)

func TestUsageSendsSafeHeadersAndParsesQuota(t *testing.T) {
	var sawAuth, sawAccount, sawReferer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		sawAuth = r.Header.Get("Authorization")
		sawAccount = r.Header.Get("ChatGPT-Account-Id")
		sawReferer = r.Header.Get("Referer")
		json.NewEncoder(w).Encode(map[string]any{
			"planType": "pro",
			"rateLimits": map[string]any{
				"primary": map[string]any{"usedPercent": 44, "resetsAt": "2026-06-11T20:00:00Z"},
			},
		})
	}))
	defer srv.Close()

	result, err := Client{BaseURL: srv.URL}.Usage(context.Background(), "codex-test", secret.Bundle{
		AuthMode:    "oauth",
		AccessToken: "access-secret",
		AccountID:   "acct_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sawAuth != "Bearer access-secret" || sawAccount != "acct_test" || sawReferer != "https://chatgpt.com/" {
		t.Fatalf("headers auth=%q account=%q referer=%q", sawAuth, sawAccount, sawReferer)
	}
	if result.Quota.Plan != "pro" || result.Quota.PrimaryUsedPercent != 44 {
		t.Fatalf("quota = %+v", result.Quota)
	}
}

func TestUsageRequiresOAuthToken(t *testing.T) {
	if _, err := (Client{}).Usage(context.Background(), "codex-test", secret.Bundle{}); err == nil {
		t.Fatal("expected missing token error")
	}
}
