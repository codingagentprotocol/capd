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
	maxCredentialBlobSize   = 5 * 512
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

type credentialChunkManifest struct {
	Format string `json:"format"`
	Chunks int    `json:"chunks"`
}

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
	if err := credentialWriteBundle(targetName(ref.ID), data); err != nil {
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
	data, err := credentialReadBundle(targetName(cleanID(ref.ID)))
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
	return credentialDeleteBundle(targetName(cleanID(ref.ID)))
}

func credentialWriteBundle(target string, data []byte) error {
	if err := credentialDeleteBundle(target); err != nil {
		return err
	}
	if len(data) <= maxCredentialBlobSize {
		return credentialWrite(target, data)
	}
	chunks := splitCredentialBlob(data)
	for i, chunk := range chunks {
		if err := credentialWrite(chunkTargetName(target, i), chunk); err != nil {
			_ = credentialDeleteChunks(target, len(chunks))
			_ = credentialDelete(target)
			return err
		}
	}
	manifest, err := json.Marshal(credentialChunkManifest{Format: "capd-windows-secret-chunks-v1", Chunks: len(chunks)})
	if err != nil {
		_ = credentialDeleteChunks(target, len(chunks))
		_ = credentialDelete(target)
		return err
	}
	if err := credentialWrite(target, manifest); err != nil {
		_ = credentialDeleteChunks(target, len(chunks))
		_ = credentialDelete(target)
		return err
	}
	return nil
}

func credentialReadBundle(target string) ([]byte, error) {
	data, err := credentialRead(target)
	if err != nil {
		return nil, err
	}
	manifest, ok := parseCredentialChunkManifest(data)
	if !ok {
		return data, nil
	}
	var out []byte
	for i := 0; i < manifest.Chunks; i++ {
		chunk, err := credentialRead(chunkTargetName(target, i))
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
	}
	return out, nil
}

func credentialDeleteBundle(target string) error {
	data, err := credentialRead(target)
	if err != nil && err != errorNotFound {
		return err
	}
	if manifest, ok := parseCredentialChunkManifest(data); ok {
		if err := credentialDeleteChunks(target, manifest.Chunks); err != nil {
			return err
		}
	}
	if err := credentialDelete(target); err != nil && err != errorNotFound {
		return err
	}
	return nil
}

func credentialDeleteChunks(target string, chunks int) error {
	for i := 0; i < chunks; i++ {
		if err := credentialDelete(chunkTargetName(target, i)); err != nil && err != errorNotFound {
			return err
		}
	}
	return nil
}

func splitCredentialBlob(data []byte) [][]byte {
	chunks := make([][]byte, 0, (len(data)+maxCredentialBlobSize-1)/maxCredentialBlobSize)
	for len(data) > 0 {
		n := maxCredentialBlobSize
		if len(data) < n {
			n = len(data)
		}
		chunks = append(chunks, data[:n])
		data = data[n:]
	}
	return chunks
}

func parseCredentialChunkManifest(data []byte) (credentialChunkManifest, bool) {
	var manifest credentialChunkManifest
	if len(data) == 0 || json.Unmarshal(data, &manifest) != nil {
		return credentialChunkManifest{}, false
	}
	if manifest.Format != "capd-windows-secret-chunks-v1" || manifest.Chunks <= 0 {
		return credentialChunkManifest{}, false
	}
	return manifest, true
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

func chunkTargetName(target string, index int) string {
	return fmt.Sprintf("%s/chunk/%04d", target, index)
}
