//go:build linux

package secret

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxNativeStoreUsesSecretToolStdin(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	storePath := filepath.Join(dir, "secret.json")
	toolPath := filepath.Join(dir, "secret-tool")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "` + logPath + `"
case "$1" in
  store)
    cat > "` + storePath + `"
    ;;
  lookup)
    cat "` + storePath + `"
    printf '\n'
    ;;
  clear)
    rm -f "` + storePath + `"
    ;;
  *)
    exit 9
    ;;
esac
`
	if err := os.WriteFile(toolPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	st := nativeStore{tool: toolPath}
	ref, err := st.Put(context.Background(), "acct/one", Bundle{
		Provider:    "codex",
		AuthMode:    "oauth",
		AccessToken: "linux-access-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "linux-access-secret" {
		t.Fatalf("bundle = %+v", got)
	}
	if err := st.Delete(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "linux-access-secret") {
		t.Fatalf("secret leaked into argv log: %s", calls)
	}
	if !strings.Contains(string(calls), "store --label capd acct-one service capd.account.secrets account acct-one") {
		t.Fatalf("store call = %s", calls)
	}
}
