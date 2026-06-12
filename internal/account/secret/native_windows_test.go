//go:build windows

package secret

import (
	"testing"
)

func TestWindowsCredentialBlobChunking(t *testing.T) {
	data := make([]byte, maxCredentialBlobSize*2+17)
	for i := range data {
		data[i] = byte(i % 251)
	}
	chunks := splitCredentialBlob(data)
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	for i, chunk := range chunks {
		if len(chunk) > maxCredentialBlobSize {
			t.Fatalf("chunk %d length = %d, max %d", i, len(chunk), maxCredentialBlobSize)
		}
	}
	var joined []byte
	for _, chunk := range chunks {
		joined = append(joined, chunk...)
	}
	if string(joined) != string(data) {
		t.Fatal("chunks did not roundtrip original bytes")
	}
}

func TestWindowsCredentialChunkManifestParsing(t *testing.T) {
	manifest, ok := parseCredentialChunkManifest([]byte(`{"format":"capd-windows-secret-chunks-v1","chunks":3}`))
	if !ok || manifest.Chunks != 3 {
		t.Fatalf("manifest = %+v ok=%t", manifest, ok)
	}
	for _, raw := range [][]byte{
		[]byte(`{"provider":"codex","accessToken":"not-a-manifest"}`),
		[]byte(`{"format":"capd-windows-secret-chunks-v1","chunks":0}`),
		[]byte(`not-json`),
		nil,
	} {
		if manifest, ok := parseCredentialChunkManifest(raw); ok {
			t.Fatalf("unexpected manifest for %q: %+v", raw, manifest)
		}
	}
}
