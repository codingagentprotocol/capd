package secret

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
)

func TestNativeStoreRoundTrip(t *testing.T) {
	if os.Getenv("CAPD_TEST_NATIVE_SECRET") != "1" {
		t.Skip("set CAPD_TEST_NATIVE_SECRET=1 to exercise the OS secret backend")
	}
	st, err := Open(t.TempDir(), BackendNative)
	if errors.Is(err, ErrNativeUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	ref, err := st.Put(context.Background(), "test-"+randomTestID(t), Bundle{
		Provider:     "codex",
		AuthMode:     "oauth",
		AccessToken:  "native-access-secret",
		RefreshToken: "native-refresh-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = st.Delete(context.Background(), ref)
	})
	if ref.Backend != BackendNative {
		t.Fatalf("backend = %q", ref.Backend)
	}
	got, err := st.Get(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "native-access-secret" || got.RefreshToken != "native-refresh-secret" {
		t.Fatalf("bundle = %+v", got)
	}
	if err := st.Delete(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(context.Background(), ref); err == nil {
		t.Fatal("expected get after delete to fail")
	}
}

func randomTestID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}
