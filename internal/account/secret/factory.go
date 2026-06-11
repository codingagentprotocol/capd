package secret

import (
	"errors"
	"fmt"
	"os"
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
	switch backend {
	case "", BackendFile:
		return NewFileStore(root), nil
	case BackendNative:
		return nil, ErrNativeUnavailable
	default:
		return nil, fmt.Errorf("unknown secret backend %q", backend)
	}
}
