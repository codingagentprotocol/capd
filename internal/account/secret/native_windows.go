//go:build windows

package secret

import (
	"context"
	"encoding/json"
	"fmt"
	"syscall"
	"unsafe"
)

const nativeService = "capd.account.secrets"

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
	errorNotFound           = syscall.Errno(1168)
)

var (
	advapi32        = syscall.NewLazyDLL("advapi32.dll")
	procCredWriteW  = advapi32.NewProc("CredWriteW")
	procCredReadW   = advapi32.NewProc("CredReadW")
	procCredDeleteW = advapi32.NewProc("CredDeleteW")
	procCredFree    = advapi32.NewProc("CredFree")
)

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
	if err := credentialWrite(targetName(ref.ID), data); err != nil {
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
	data, err := credentialRead(targetName(cleanID(ref.ID)))
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
	err := credentialDelete(targetName(cleanID(ref.ID)))
	if err == errorNotFound {
		return nil
	}
	return err
}

type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

func credentialWrite(target string, data []byte) error {
	targetPtr, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	userPtr, err := syscall.UTF16PtrFromString("capd")
	if err != nil {
		return err
	}
	var blob *byte
	if len(data) > 0 {
		blob = &data[0]
	}
	cred := credential{
		Type:               credTypeGeneric,
		TargetName:         targetPtr,
		CredentialBlobSize: uint32(len(data)),
		CredentialBlob:     blob,
		Persist:            credPersistLocalMachine,
		UserName:           userPtr,
	}
	r1, _, err := procCredWriteW.Call(uintptr(unsafe.Pointer(&cred)), 0)
	if r1 == 0 {
		return err
	}
	return nil
}

func credentialRead(target string) ([]byte, error) {
	targetPtr, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return nil, err
	}
	var cred *credential
	r1, _, err := procCredReadW.Call(
		uintptr(unsafe.Pointer(targetPtr)),
		uintptr(credTypeGeneric),
		0,
		uintptr(unsafe.Pointer(&cred)),
	)
	if r1 == 0 {
		return nil, err
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(cred)))
	if cred.CredentialBlobSize == 0 {
		return nil, nil
	}
	return append([]byte(nil), unsafe.Slice(cred.CredentialBlob, cred.CredentialBlobSize)...), nil
}

func credentialDelete(target string) error {
	targetPtr, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	r1, _, err := procCredDeleteW.Call(
		uintptr(unsafe.Pointer(targetPtr)),
		uintptr(credTypeGeneric),
		0,
	)
	if r1 == 0 {
		return err
	}
	return nil
}

func targetName(id string) string {
	return nativeService + "/" + id
}
