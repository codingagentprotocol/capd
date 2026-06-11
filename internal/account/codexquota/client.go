package codexquota

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/codingagentprotocol/capd/internal/account"
	codexheaders "github.com/codingagentprotocol/capd/internal/account/codex"
	"github.com/codingagentprotocol/capd/internal/account/secret"
)

const DefaultBaseURL = "https://chatgpt.com"

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

type Result struct {
	Usage map[string]any
	Quota account.QuotaSnapshot
}

func (c Client) Usage(ctx context.Context, accountID string, bundle secret.Bundle) (Result, error) {
	token := bundle.AccessToken
	if token == "" {
		token = bundle.IDToken
	}
	if token == "" {
		return Result{}, fmt.Errorf("codex quota: OAuth access token is required")
	}
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	headers, err := codexheaders.BuildHeaders(codexheaders.RequestQuota, codexheaders.Identity{
		AccountID: bundle.AccountID,
		AuthMode:  codexheaders.AuthModeOAuth,
	}, token, codexheaders.Options{})
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/backend-api/wham/usage", nil)
	if err != nil {
		return Result{}, err
	}
	req.Header = headers
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("codex quota: wham usage returned HTTP %d", resp.StatusCode)
	}
	var usage map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&usage); err != nil {
		return Result{}, err
	}
	return Result{
		Usage: usage,
		Quota: account.QuotaFromUsage(accountID, usage),
	}, nil
}
