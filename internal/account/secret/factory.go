package secret

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	BackendFile   = "file"
	BackendNative = "native"
	EnvBackend    = "CAPD_SECRET_BACKEND"
)

var ErrNativeUnavailable = errors.New("native secret backend is not available in this build")

func Open(root, backend string) (Store, error) {
	if backend == "" {
		backend = os.Getenv(EnvBackend)
	}
	backend = strings.TrimSpace(backend)
	switch backend {
	case "", BackendFile:
		return NewFileStore(root), nil
	case BackendNative:
		return openNative(root)
	default:
		return nil, fmt.Errorf("unknown secret backend %q", backend)
	}
}

func NormalizeBackend(backend string) (string, error) {
	backend = strings.TrimSpace(backend)
	switch backend {
	case "":
		return "", nil
	case BackendFile, BackendNative:
		return backend, nil
	default:
		return "", fmt.Errorf("unknown secret backend %q", backend)
	}
}
