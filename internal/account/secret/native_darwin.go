//go:build darwin && cgo

package secret

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"unsafe"
)

const nativeService = "capd.account.secrets"

type nativeStore struct{}

func openNative(_ string) (Store, error) {
	return nativeStore{}, nil
}

func (nativeStore) Backend() string { return BackendNative }

func (st nativeStore) Put(ctx context.Context, id string, bundle Bundle) (Ref, error) {
	if err := ctx.Err(); err != nil {
		return Ref{}, err
	}
	if id == "" {
		return Ref{}, fmt.Errorf("secret id is required")
	}
	ref := Ref{Backend: st.Backend(), ID: cleanID(id)}
	data, err := json.Marshal(bundle)
	if err != nil {
		return Ref{}, err
	}
	if err := st.Delete(ctx, ref); err != nil {
		return Ref{}, err
	}
	if err := keychainAdd(nativeService, ref.ID, data); err != nil {
		return Ref{}, err
	}
	return ref, nil
}

func (st nativeStore) Get(ctx context.Context, ref Ref) (Bundle, error) {
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}
	if ref.Backend != "" && ref.Backend != st.Backend() {
		return Bundle{}, fmt.Errorf("secret backend %q is not %q", ref.Backend, st.Backend())
	}
	data, err := keychainFind(nativeService, cleanID(ref.ID))
	if err != nil {
		return Bundle{}, err
	}
	var bundle Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func (st nativeStore) Delete(ctx context.Context, ref Ref) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ref.Backend != "" && ref.Backend != st.Backend() {
		return fmt.Errorf("secret backend %q is not %q", ref.Backend, st.Backend())
	}
	err := keychainDelete(nativeService, cleanID(ref.ID))
	if isKeychainNotFound(err) {
		return nil
	}
	return err
}

func keychainAdd(service, account string, password []byte) error {
	svc := []byte(service)
	acc := []byte(account)
	var keychain C.SecKeychainRef
	return osStatus(C.SecKeychainAddGenericPassword(
		keychain,
		C.UInt32(len(svc)), (*C.char)(cBytes(svc)),
		C.UInt32(len(acc)), (*C.char)(cBytes(acc)),
		C.UInt32(len(password)), cBytes(password),
		(*C.SecKeychainItemRef)(nil),
	))
}

func keychainFind(service, account string) ([]byte, error) {
	svc := []byte(service)
	acc := []byte(account)
	var keychain C.SecKeychainRef
	var length C.UInt32
	var data unsafe.Pointer
	status := C.SecKeychainFindGenericPassword(
		C.CFTypeRef(keychain),
		C.UInt32(len(svc)), (*C.char)(cBytes(svc)),
		C.UInt32(len(acc)), (*C.char)(cBytes(acc)),
		&length,
		&data,
		(*C.SecKeychainItemRef)(nil),
	)
	if err := osStatus(status); err != nil {
		return nil, err
	}
	defer C.SecKeychainItemFreeContent((*C.SecKeychainAttributeList)(nil), data)
	return C.GoBytes(data, C.int(length)), nil
}

func keychainDelete(service, account string) error {
	svc := []byte(service)
	acc := []byte(account)
	var keychain C.SecKeychainRef
	var item C.SecKeychainItemRef
	status := C.SecKeychainFindGenericPassword(
		C.CFTypeRef(keychain),
		C.UInt32(len(svc)), (*C.char)(cBytes(svc)),
		C.UInt32(len(acc)), (*C.char)(cBytes(acc)),
		(*C.UInt32)(nil),
		(*unsafe.Pointer)(nil),
		&item,
	)
	if err := osStatus(status); err != nil {
		return err
	}
	defer C.CFRelease(C.CFTypeRef(item))
	return osStatus(C.SecKeychainItemDelete(item))
}

func cBytes(data []byte) unsafe.Pointer {
	if len(data) == 0 {
		return nil
	}
	return unsafe.Pointer(&data[0])
}

type keychainError C.OSStatus

func (e keychainError) Error() string {
	return fmt.Sprintf("macOS keychain status %d", int32(e))
}

func osStatus(status C.OSStatus) error {
	if status == 0 {
		return nil
	}
	return keychainError(status)
}

func isKeychainNotFound(err error) bool {
	if err == nil {
		return false
	}
	if status, ok := err.(keychainError); ok {
		return int32(status) == -25300
	}
	return false
}
