//go:build (!darwin && !windows) || (darwin && !cgo)

package secret

func openNative(_ string) (Store, error) {
	return nil, ErrNativeUnavailable
}
