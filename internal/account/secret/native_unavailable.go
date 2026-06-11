//go:build (!darwin && !windows && !linux) || (darwin && !cgo)

package secret

func openNative(_ string) (Store, error) {
	return nil, ErrNativeUnavailable
}
