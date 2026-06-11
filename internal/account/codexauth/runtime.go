package codexauth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/secret"
)

type RuntimeProjector struct {
	Root    string
	Secrets secret.Store
}

type RuntimeProfile struct {
	AccountID string
	CodexHome string
	Env       []string
}

func (rp RuntimeProjector) Project(ctx context.Context, acc account.Account) (RuntimeProfile, error) {
	if rp.Root == "" {
		return RuntimeProfile{}, fmt.Errorf("runtime root is required")
	}
	if rp.Secrets == nil {
		return RuntimeProfile{}, fmt.Errorf("secret store is required")
	}
	ref, err := parseSecretRef(acc.SecretRef)
	if err != nil {
		return RuntimeProfile{}, err
	}
	bundle, err := rp.Secrets.Get(ctx, ref)
	if err != nil {
		return RuntimeProfile{}, err
	}
	dir := filepath.Join(rp.Root, Provider, safeID(acc.ID))
	authPath := filepath.Join(dir, "auth.json")
	if synced, ok := rp.syncRefreshedAuth(ctx, ref, bundle, authPath); ok {
		bundle = synced
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return RuntimeProfile{}, err
	}
	authJSON := bundle.RawAuthJSON
	if len(authJSON) == 0 {
		authJSON, err = buildAuthJSON(bundle)
		if err != nil {
			return RuntimeProfile{}, err
		}
	}
	if err := writePrivate(authPath, authJSON); err != nil {
		return RuntimeProfile{}, err
	}
	marker, _ := json.MarshalIndent(map[string]any{
		"managedBy": "capd",
		"provider":  Provider,
		"account":   acc.ID,
		"updatedAt": time.Now().UTC().Format(time.RFC3339),
	}, "", "  ")
	if err := writePrivate(filepath.Join(dir, ".capd_projection.json"), marker); err != nil {
		return RuntimeProfile{}, err
	}
	return RuntimeProfile{
		AccountID: acc.ID,
		CodexHome: dir,
		Env:       []string{"CODEX_HOME=" + dir},
	}, nil
}

func (rp RuntimeProjector) syncRefreshedAuth(ctx context.Context, ref secret.Ref, current secret.Bundle, authPath string) (secret.Bundle, bool) {
	projected, err := os.ReadFile(authPath)
	if err != nil {
		return secret.Bundle{}, false
	}
	if !authJSONNewer(projected, current.RawAuthJSON) {
		return secret.Bundle{}, false
	}
	var root any
	if err := json.Unmarshal(projected, &root); err != nil {
		return secret.Bundle{}, false
	}
	bundle := bundleFromAuthJSON(root, projected)
	if bundle.AccessToken == "" && bundle.RefreshToken == "" && bundle.IDToken == "" && bundle.APIKey == "" {
		return secret.Bundle{}, false
	}
	if bundle.Email == "" {
		bundle.Email = current.Email
	}
	if bundle.AccountID == "" {
		bundle.AccountID = current.AccountID
	}
	if _, err := rp.Secrets.Put(ctx, ref.ID, bundle); err != nil {
		return secret.Bundle{}, false
	}
	return bundle, true
}

func authJSONNewer(candidate, current json.RawMessage) bool {
	candidateTime, ok := authLastRefresh(candidate)
	if !ok {
		return false
	}
	currentTime, ok := authLastRefresh(current)
	if !ok {
		return true
	}
	return candidateTime.After(currentTime)
}

func authLastRefresh(data []byte) (time.Time, bool) {
	if len(data) == 0 {
		return time.Time{}, false
	}
	var body struct {
		LastRefresh string `json:"last_refresh"`
	}
	if err := json.Unmarshal(data, &body); err != nil || body.LastRefresh == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, body.LastRefresh)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func parseSecretRef(value string) (secret.Ref, error) {
	return secret.ParseRef(value)
}

func buildAuthJSON(bundle secret.Bundle) ([]byte, error) {
	body := map[string]any{}
	if bundle.APIKey != "" {
		body["OPENAI_API_KEY"] = bundle.APIKey
	}
	if bundle.AccessToken != "" || bundle.RefreshToken != "" || bundle.IDToken != "" || bundle.AccountID != "" {
		tokens := map[string]any{}
		if bundle.AccessToken != "" {
			tokens["access_token"] = bundle.AccessToken
		}
		if bundle.RefreshToken != "" {
			tokens["refresh_token"] = bundle.RefreshToken
		}
		if bundle.IDToken != "" {
			tokens["id_token"] = bundle.IDToken
		}
		if bundle.AccountID != "" {
			tokens["account_id"] = bundle.AccountID
		}
		body["tokens"] = tokens
	}
	return json.MarshalIndent(body, "", "  ")
}

func writePrivate(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
