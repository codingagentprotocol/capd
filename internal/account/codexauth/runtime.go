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
	if err := writePrivate(filepath.Join(dir, "auth.json"), authJSON); err != nil {
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

func parseSecretRef(value string) (secret.Ref, error) {
	if value == "" {
		return secret.Ref{}, fmt.Errorf("secret ref is empty")
	}
	for i, r := range value {
		if r == ':' {
			return secret.Ref{Backend: value[:i], ID: value[i+1:]}, nil
		}
	}
	return secret.Ref{ID: value}, nil
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
