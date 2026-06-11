package secret

import (
	"context"
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
	info, err := os.Stat(filepath.Join(root, ref.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestParseRef(t *testing.T) {
	ref, err := ParseRef("file:codex-a")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Backend != "file" || ref.ID != "codex-a" {
		t.Fatalf("ref = %+v", ref)
	}
	if _, err := ParseRef(""); err == nil {
		t.Fatal("expected empty ref error")
	}
}
