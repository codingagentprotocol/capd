package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureTokenIdempotentAndPrivate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tok1, err := EnsureToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok1) != 64 { // 32 bytes hex
		t.Fatalf("token length = %d", len(tok1))
	}
	tok2, err := EnsureToken()
	if err != nil || tok2 != tok1 {
		t.Fatalf("not idempotent: %q vs %q (%v)", tok1, tok2, err)
	}

	home, _ := Home()
	dirInfo, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("home mode = %o, want 700", dirInfo.Mode().Perm())
	}
	info, err := os.Stat(home + "/token")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode = %o, want 600", info.Mode().Perm())
	}
}

func TestEnsureTokenTightensExistingPermissions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	capdHome := filepath.Join(root, ".capd")
	if err := os.MkdirAll(capdHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(capdHome, 0o755); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(capdHome, "token")
	if err := os.WriteFile(tokenPath, []byte("existing-token\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tokenPath, 0o644); err != nil {
		t.Fatal(err)
	}

	tok, err := EnsureToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "existing-token" {
		t.Fatalf("token = %q", tok)
	}
	assertMode(t, capdHome, 0o700)
	assertMode(t, tokenPath, 0o600)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
