# Testing

capd keeps CI deterministic and leaves live-agent checks opt-in. Use the
commands below before release-sized changes.

## Standard regression

```bash
go test ./...
go vet ./...
go build ./...
go test -race ./internal/server ./internal/account/...
go test -count=5 ./internal/server ./internal/account/...
go run ./cmd/capd agents list
```

## Native SecretStore

The default test suite compiles every native backend but only touches real OS
secret storage when explicitly requested.

```bash
CAPD_TEST_NATIVE_SECRET=1 go test ./internal/account/secret -run TestNativeStoreRoundTrip -count=1
GOOS=linux GOARCH=amd64 go test -c ./internal/account/secret -o /tmp/capd-secret-linux.test
GOOS=windows GOARCH=amd64 go test -c ./internal/account/secret -o /tmp/capd-secret-windows.test.exe
CGO_ENABLED=0 go test ./internal/account/secret
```

Linux native storage requires `secret-tool` from libsecret and an unlocked
Secret Service session.

To verify Codex account smoke is actually using the native backend:

```bash
capd accounts --secret-backend native codex import
capd accounts --secret-backend native codex smoke --require-secret-backend native --json
```

## Codex Account Smoke

Import at least one Codex `auth.json`, then run the local smoke check:

```bash
capd accounts codex import
capd accounts codex smoke
capd accounts codex smoke --json
```

For real quota validation:

```bash
capd accounts codex smoke --quota
```

For multi-account routing readiness:

```bash
capd accounts codex quota all
capd accounts codex smoke --quota --require-multiple --require-fresh-quota --require-all-fresh-quota
capd accounts check --json
capd accounts check --refresh-quota --require-multiple --require-fresh-quota --require-all-fresh-quota --require-secret-backend native
capd agents usage codex --account auto
```

The smoke command verifies imported account metadata, SecretStore readability,
per-account `CODEX_HOME` projection, runtime `CODEX_HOME` env, private
`auth.json` permissions, capd projection marker integrity, auto-route account
selection, and optionally ChatGPT backend quota refresh. It prints only account
metadata, projection paths, quota percentages, projection booleans, and the
selected `autoRoute.accountId`, `autoRoute.quotaState` (`fresh`, `stale`, or
`missing`), plus `secretBackend`; per-account rows include
`secretBackendOk`, `secretReadable`, `quotaState`, `quotaFresh`, and
`quotaCheckedAt` fields. Token material is never printed. Use
`--require-fresh-quota` to fail unless the auto-route decision is backed by a
fresh cached quota snapshot; use `--require-all-fresh-quota` to fail unless
every imported account has fresh cached quota. Use
`--require-secret-backend native` to fail unless smoke is reading credentials
from the OS secret backend.
Projection, quota refresh, and smoke all fail closed if an account `secret_ref`
points at a different backend than the active SecretStore. Use `--json` to
capture machine-readable smoke evidence in long tasks or CI logs.

Use `capd accounts check --json` when you want the same safe evidence through
the running daemon and CAP WebSocket path used by web clients. Unlike
`capd accounts codex smoke --quota`, it does not refresh remote quota; it
checks cached quota freshness, SecretStore readability, runtime projection, and
auto-route evidence without returning runtime paths or token material.
Use `capd accounts check --refresh-quota` when you want a single daemon-side
readiness gate to refresh every imported Codex account through `accounts/quota`
before checking cached freshness, without printing raw backend usage JSON.
Use its `--require-*` flags as a daemon-side readiness gate: the command exits
non-zero when too few accounts are imported, auto-route quota is stale or
missing, any checked account lacks fresh cached quota, or the daemon is using a
different SecretStore backend than expected.
The Web Console's `就绪门禁` button applies the same readiness checks to the
`accounts/check` evidence, with an optional native SecretStore requirement.
Use `刷新全部 quota` first when the gate reports stale or missing cached quota.
