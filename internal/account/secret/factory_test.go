package secret

import (
	"errors"
	"testing"
)

func TestOpenDefaultsToFileStore(t *testing.T) {
	st, err := Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Backend() != BackendFile {
		t.Fatalf("backend = %q", st.Backend())
	}
}

func TestOpenNativeUnavailableIsExplicit(t *testing.T) {
	if _, err := Open(t.TempDir(), BackendNative); !errors.Is(err, ErrNativeUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenRejectsUnknownBackend(t *testing.T) {
	if _, err := Open(t.TempDir(), "mystery"); err == nil {
		t.Fatal("expected unknown backend error")
	}
}
