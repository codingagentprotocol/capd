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

func TestOpenTrimsBackendFromEnvAndArgument(t *testing.T) {
	t.Setenv(EnvBackend, " file ")
	st, err := Open(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Backend() != BackendFile {
		t.Fatalf("env backend = %q", st.Backend())
	}

	st, err = Open(t.TempDir(), " file ")
	if err != nil {
		t.Fatal(err)
	}
	if st.Backend() != BackendFile {
		t.Fatalf("arg backend = %q", st.Backend())
	}
}

func TestOpenNativeUnavailableIsExplicit(t *testing.T) {
	st, err := Open(t.TempDir(), BackendNative)
	if err == nil {
		if st.Backend() != BackendNative {
			t.Fatalf("backend = %q", st.Backend())
		}
		return
	}
	if !errors.Is(err, ErrNativeUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenRejectsUnknownBackend(t *testing.T) {
	if _, err := Open(t.TempDir(), "mystery"); err == nil {
		t.Fatal("expected unknown backend error")
	}
}

func TestNormalizeBackend(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"", ""},
		{" file ", BackendFile},
		{" native ", BackendNative},
	} {
		got, err := NormalizeBackend(tc.in)
		if err != nil {
			t.Fatalf("%q err = %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("%q = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := NormalizeBackend("mystery"); err == nil {
		t.Fatal("expected unknown backend error")
	}
}
