package daemon

import (
	"os"
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
	info, err := os.Stat(home + "/token")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode = %o, want 600", info.Mode().Perm())
	}
}
