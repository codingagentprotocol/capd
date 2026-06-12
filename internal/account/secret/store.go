package secret

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Ref struct {
	Backend string
	ID      string
}

func (r Ref) String() string {
	if r.Backend == "" {
		return r.ID
	}
	return r.Backend + ":" + r.ID
}

func ParseRef(value string) (Ref, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Ref{}, fmt.Errorf("secret ref is empty")
	}
	for i, r := range value {
		if r == ':' {
			backend := strings.TrimSpace(value[:i])
			id := strings.TrimSpace(value[i+1:])
			if backend == "" {
				return Ref{}, fmt.Errorf("secret backend is empty")
			}
			if id == "" {
				return Ref{}, fmt.Errorf("secret id is empty")
			}
			return Ref{Backend: backend, ID: id}, nil
		}
	}
	id := strings.TrimSpace(value)
	if id == "" {
		return Ref{}, fmt.Errorf("secret id is empty")
	}
	return Ref{ID: id}, nil
}

type Bundle struct {
	Provider     string          `json:"provider"`
	AuthMode     string          `json:"authMode"`
	AccessToken  string          `json:"accessToken,omitempty"`
	RefreshToken string          `json:"refreshToken,omitempty"`
	IDToken      string          `json:"idToken,omitempty"`
	APIKey       string          `json:"apiKey,omitempty"`
	AccountID    string          `json:"accountId,omitempty"`
	Email        string          `json:"email,omitempty"`
	RawAuthJSON  json.RawMessage `json:"rawAuthJson,omitempty"`
}

type Store interface {
	Put(ctx context.Context, id string, bundle Bundle) (Ref, error)
	Get(ctx context.Context, ref Ref) (Bundle, error)
	Delete(ctx context.Context, ref Ref) error
	Backend() string
}

func EnsureRefBackend(st Store, ref Ref) error {
	if st == nil {
		return fmt.Errorf("secret store is required")
	}
	if ref.Backend != "" && ref.Backend != st.Backend() {
		return fmt.Errorf("secret backend = %q, active backend = %q", ref.Backend, st.Backend())
	}
	return nil
}

type FileStore struct {
	root string
}

func NewFileStore(root string) *FileStore {
	return &FileStore{root: root}
}

func (st *FileStore) Backend() string { return BackendFile }

func (st *FileStore) Put(_ context.Context, id string, bundle Bundle) (Ref, error) {
	if id == "" {
		return Ref{}, fmt.Errorf("secret id is required")
	}
	if err := os.MkdirAll(st.root, 0o700); err != nil {
		return Ref{}, err
	}
	if err := os.Chmod(st.root, 0o700); err != nil {
		return Ref{}, err
	}
	ref := Ref{Backend: st.Backend(), ID: cleanID(id)}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return Ref{}, err
	}
	path := st.path(ref)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return Ref{}, err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return Ref{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return Ref{}, err
	}
	return ref, nil
}

func (st *FileStore) Get(_ context.Context, ref Ref) (Bundle, error) {
	if ref.Backend != "" && ref.Backend != st.Backend() {
		return Bundle{}, fmt.Errorf("secret backend %q is not %q", ref.Backend, st.Backend())
	}
	data, err := os.ReadFile(st.path(ref))
	if err != nil {
		return Bundle{}, err
	}
	var bundle Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func (st *FileStore) Delete(_ context.Context, ref Ref) error {
	if ref.Backend != "" && ref.Backend != st.Backend() {
		return fmt.Errorf("secret backend %q is not %q", ref.Backend, st.Backend())
	}
	err := os.Remove(st.path(ref))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (st *FileStore) path(ref Ref) string {
	return filepath.Join(st.root, cleanID(ref.ID)+".json")
}

func cleanID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, id)
	if id == "" {
		return "secret"
	}
	return id
}
