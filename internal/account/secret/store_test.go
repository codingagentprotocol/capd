package secret

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreRoundTripAndPermissions(t *testing.T) {
	root := t.TempDir()
	st := NewFileStore(root)

	ref, err := st.Put(context.Background(), "codex/account:one", Bundle{
		Provider:     "codex",
		AuthMode:     "oauth",
		AccessToken:  "access-secret",
		RefreshToken: "refresh-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Backend != "file" {
		t.Fatalf("backend = %q", ref.Backend)
	}
	got, err := st.Get(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "access-secret" || got.RefreshToken != "refresh-secret" {
		t.Fatalf("bundle = %+v", got)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if rootInfo.Mode().Perm() != 0o700 {
		t.Fatalf("root mode = %o", rootInfo.Mode().Perm())
	}
	info, err := os.Stat(filepath.Join(root, ref.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestFileStoreTightensExistingRootPermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	st := NewFileStore(root)
	if _, err := st.Put(context.Background(), "codex-a", Bundle{Provider: "codex", AccessToken: "access-secret"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("root mode = %o", info.Mode().Perm())
	}
}

func TestFileStoreGetTightensExistingPermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	ref := Ref{Backend: BackendFile, ID: "codex-a"}
	data, err := json.Marshal(Bundle{
		Provider:    "codex",
		AuthMode:    "oauth",
		AccessToken: "access-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ref.ID+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := NewFileStore(root).Get(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "access-secret" {
		t.Fatalf("bundle = %+v", got)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if rootInfo.Mode().Perm() != 0o700 {
		t.Fatalf("root mode = %o", rootInfo.Mode().Perm())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestParseRef(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    Ref
		wantErr bool
	}{
		{name: "backend and id", value: "file:codex-a", want: Ref{Backend: "file", ID: "codex-a"}},
		{name: "plain id", value: "codex-a", want: Ref{ID: "codex-a"}},
		{name: "trim parts", value: " native : codex-a ", want: Ref{Backend: "native", ID: "codex-a"}},
		{name: "empty ref", value: "", wantErr: true},
		{name: "empty backend", value: ":codex-a", wantErr: true},
		{name: "empty id", value: "file:", wantErr: true},
		{name: "space id", value: "file:  ", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := ParseRef(tt.value)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ref != tt.want {
				t.Fatalf("ref = %+v, want %+v", ref, tt.want)
			}
		})
	}
}

func TestEnsureRefBackend(t *testing.T) {
	st := NewFileStore(t.TempDir())
	if err := EnsureRefBackend(st, Ref{Backend: BackendFile, ID: "codex-a"}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureRefBackend(st, Ref{ID: "codex-a"}); err != nil {
		t.Fatal(err)
	}
	err := EnsureRefBackend(st, Ref{Backend: BackendNative, ID: "codex-a"})
	if err == nil || err.Error() != `secret backend = "native", active backend = "file"` {
		t.Fatalf("err = %v", err)
	}
	if err := EnsureRefBackend(nil, Ref{Backend: BackendFile, ID: "codex-a"}); err == nil || err.Error() != "secret store is required" {
		t.Fatalf("nil store err = %v", err)
	}
}
