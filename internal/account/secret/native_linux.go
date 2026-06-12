//go:build linux

package secret

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

const nativeService = "capd.account.secrets"

const defaultSecretTool = "secret-tool"

type nativeStore struct {
	tool string
}

func openNative(_ string) (Store, error) {
	tool, err := exec.LookPath(defaultSecretTool)
	if err != nil {
		return nil, fmt.Errorf("%w: %s not found", ErrNativeUnavailable, defaultSecretTool)
	}
	return nativeStore{tool: tool}, nil
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
	if err := st.run(ctx, data, "store", "--label", nativeLabel(ref.ID), "service", nativeService, "account", ref.ID); err != nil {
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
	data, err := st.output(ctx, "lookup", "service", nativeService, "account", cleanID(ref.ID))
	if err != nil {
		return Bundle{}, err
	}
	data = bytes.TrimSuffix(data, []byte{'\n'})
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
	return st.run(ctx, nil, "clear", "service", nativeService, "account", cleanID(ref.ID))
}

func (st nativeStore) run(ctx context.Context, stdin []byte, args ...string) error {
	cmd := exec.CommandContext(ctx, st.tool, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		if stdin != nil {
			return fmt.Errorf("%s %s: %w: command output omitted because secret stdin was provided", defaultSecretTool, safeArgs(args), err)
		}
		return fmt.Errorf("%s %s: %w%s", defaultSecretTool, safeArgs(args), err, safeCommandOutput(out))
	}
	return nil
}

func (st nativeStore) output(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, st.tool, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", defaultSecretTool, safeArgs(args), err)
	}
	return out, nil
}

func nativeLabel(id string) string {
	return "capd " + id
}

func safeArgs(args []string) string {
	return strings.Join(args, " ")
}

func safeCommandOutput(out []byte) string {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return ""
	}
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return ": " + text
}
