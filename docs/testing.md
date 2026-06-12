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

The same deterministic core suite is available as:

```bash
make verify
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

Or:

```bash
make verify-secretstore
```

Linux native storage requires `secret-tool` from libsecret and an unlocked
Secret Service session. The Linux native tests also lock the safety contract
that token bundles go through stdin and that failing `secret-tool store`
commands omit command output, so a helper that echoes stdin to stderr cannot
leak access or refresh tokens into capd errors.
Windows native storage uses Credential Manager. Small bundles are stored as a
single credential for backward compatibility; larger Codex auth bundles are
split into bounded chunk credentials with a small manifest credential, avoiding
the Windows credential blob size limit while keeping reads transparent.

To verify Codex account smoke is actually using the native backend:

```bash
capd accounts --secret-backend native codex import
capd accounts --secret-backend native codex smoke --require-secret-backend native --json --timeout 2m
```

For accounts that were already imported with the file backend, migrate the
stored token bundle into the native backend first:

```bash
capd accounts codex migrate-secrets --from file --to native --dry-run
capd accounts codex migrate-secrets --from file --to native --timeout 2m
CAPD_SECRET_BACKEND=native capd accounts --secret-backend native codex smoke --require-secret-backend native --json --timeout 2m
```

## Codex Account Smoke

Import at least one Codex `auth.json`, then run the local smoke check:

```bash
capd accounts codex import
capd accounts codex import --auth /tmp/acct-a/auth.json --auth /tmp/acct-b/auth.json
CAPD_CODEX_AUTH_PATHS="/tmp/acct-a/auth.json:/tmp/acct-b/auth.json" capd accounts codex import
capd accounts codex smoke
capd accounts codex smoke --json
```

Repeat `--auth` for explicit multi-account imports. `CAPD_CODEX_AUTH_PATHS`
uses the OS path-list separator (`:` on macOS/Linux, `;` on Windows) and is
only used when `--auth` is not supplied.

For real quota validation:

```bash
capd accounts codex smoke --quota
```

For multi-account routing readiness:

```bash
CAPD_SECRET_BACKEND=native capd accounts --secret-backend native codex quota all --timeout 2m
CAPD_SECRET_BACKEND=native capd accounts --secret-backend native codex smoke --json --quota --require-multiple --require-fresh-quota --require-all-fresh-quota --require-secret-backend native --timeout 2m
CAPD_SECRET_BACKEND=native capd secretstore check --json --roundtrip --require-backend native --timeout 2m
CAPD_SECRET_BACKEND=native capd doctor --json --fail --verify-secretstore --require-secret-backend native --timeout 2m

# In another terminal, keep the daemon running for CAP/WebSocket checks:
capd start --secret-backend native

capd console --probe --require-secret-backend native
capd probe data --json --readiness --require-secret-backend native --timeout 2m --fail
curl -H "Authorization: Bearer $(cat ~/.capd/token)" http://127.0.0.1:7777/probe/data
curl -H "Authorization: Bearer $(cat ~/.capd/token)" "http://127.0.0.1:7777/probe/data?readiness=1&requireSecretBackend=native"
capd health --json --require-secret-backend native
capd accounts import --auth /tmp/acct-a/auth.json --auth /tmp/acct-b/auth.json
capd accounts check --json
capd accounts check --json --readiness
capd agents usage codex --account auto
capd agents route --account auto --require-fresh-quota
capd run --agent codex --account auto --require-fresh-quota "say ready"
```

After importing multiple accounts and starting the daemon in another terminal,
the same live preflight is available without sending a prompt:

```bash
make live-codex-preflight
```

The full live chain, including the final prompt, is available as:

```bash
make live-codex-readiness
```

Both live targets run every command with the same `CAPD_SECRET_BACKEND` value
and run the daemon-side readiness gate as JSON. If a gate fails, the command
still exits non-zero, but the JSON output may include safe partial
`accounts/check` evidence under `data` so you can see which account, quota, or
SecretStore check failed without exposing tokens or local runtime paths.

Without real Codex accounts, run the deterministic simulated gate:

```bash
make verify-codex-readiness-sim
```

It exercises multi-account quota refresh, conservative auto-route selection,
fresh-quota enforcement, concurrent quota refresh with route-candidate reads,
concurrent CAP `accounts/quota all` plus fresh auto-route calls with secret-leak
guards, quota RawJSON redaction, invalid quota percent hardening, daemon-side
`accounts/check` readiness, doctor CAP/WebSocket account checks, WebSocket
disconnect/reconnect session continuity, Web probe readiness summaries, probe native SecretStore defaults,
`/healthz` backend gates, direct smoke route-candidate evidence, CLI shortcut
parameters, direct SecretStore JSON roundtrip, migration
readback-before-metadata-update safety, browser token cleanup documentation, and
secret-leak guards with local test backends.

`make live-codex-preflight` first verifies the selected SecretStore backend,
checks that at least two Codex accounts are imported, refreshes every Codex
quota, then runs `capd doctor --json --fail` against fresh local evidence before
the daemon/Web readiness chain. It also validates the tokenized Web probe URL
with the same SecretStore backend requirement before fetching `/probe/data`, and
prints the final auto-route gate as JSON so the log preserves sorted
`routeCandidates` evidence.
`make live-codex-readiness` runs that preflight before the final live prompt.
Override the prompt with
`LIVE_PROMPT="..." make live-codex-readiness`. Override the backend with
`LIVE_SECRET_BACKEND=file` only when intentionally testing the file SecretStore
path; the default live backend is `native`.
When doctor reports missing accounts and the daemon is running, prefer
`capd accounts import --auth ...` so the import uses the same CAP/WebSocket path
as the Web Console. `capd accounts codex import` remains available for direct
local imports when the daemon is not running.

`capd secretstore check --json --roundtrip --require-backend native --timeout 2m` is the
smallest direct native SecretStore gate. It opens the active backend, writes,
reads, and deletes a diagnostic secret, and fails before account checks if the
daemon/live shell is using the wrong backend or an OS credential prompt stalls
native access. `capd doctor --json --fail
--verify-secretstore --require-secret-backend native --timeout 2m`
is the recommended preflight before the live chain. It does not refresh quota or
read token material into the output; it reports daemon health, Codex CLI
availability, imported account count, cached quota freshness, auto-route
freshness, SecretStore backend, per-account SecretStore credential readability
with safe `secretState` categories such as `backend-mismatch` or `timeout`,
an explicit SecretStore write/read/delete roundtrip, daemon-side CAP
`accounts/check` reachability, readiness issues, and concrete next steps. Its
JSON also includes the same `routeCandidates` ordering used by
`agents/route --account auto`, so live preflight evidence explains why one
account would be selected. The top-level `summary` object is the compact
CI/Web view of missing accounts, account credential readability, quota
freshness, auto-route freshness, SecretStore backend status, and daemon CAP
reachability. After fixing account or quota issues, use
`capd accounts check --json --readiness` to
refresh and verify the daemon-side readiness gate before the final live run,
with safe partial evidence printed on failure.

The smoke command verifies imported account metadata, SecretStore readability,
per-account `CODEX_HOME` projection, runtime `CODEX_HOME` env, private
`auth.json` permissions, capd projection marker integrity, auto-route account
selection, and optionally ChatGPT backend quota refresh. It prints only account
metadata, projection paths, quota percentages, projection booleans, and the
selected `autoRoute.accountId`, `autoRoute.quotaState` (`fresh`, `stale`, or
`missing`), sorted `routeCandidates`, plus `secretBackend`; per-account rows include
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
the running daemon and CAP WebSocket path used by web clients; keep the daemon
running with `capd start` in another terminal before invoking it. Unlike
`capd accounts codex smoke --quota`, it does not refresh remote quota unless
`--refresh-quota` is set; it checks cached quota freshness, SecretStore
readability, runtime projection, and auto-route evidence without returning
runtime paths or token material.
Use `capd accounts check --json --readiness` when you want a single daemon-side
readiness gate to refresh every imported Codex account through `accounts/quota`
before checking cached freshness, without printing raw backend usage JSON. The
JSON form preserves safe partial evidence when a gate fails.
It exits non-zero when too few accounts are imported, auto-route quota is stale
or missing, any checked account lacks fresh cached quota, or the daemon is not
using the expected native SecretStore backend. Override that backend with
`--require-secret-backend file` only when intentionally testing the file
backend.
The Web Console's `就绪门禁` button applies the same daemon-side
`accounts/check` readiness gate, with an optional native SecretStore
requirement. It asks the daemon to refresh every imported Codex account before
checking freshness; use `刷新全部 quota` for an explicit refresh outside the
readiness gate. `capd probe data --json --readiness --timeout 2m --fail` is the
automation wrapper around `/probe/data`; both expose the same safe diagnostics
for web clients and smoke tests over HTTP, but require an `Authorization: Bearer`
header so daemon tokens are not embedded in diagnostics URLs. The HTTP handler
also has a server-side deadline: 12s for ordinary probes and 2m for readiness.
Its JSON includes a compact `summary` with account counts, quota freshness,
auto-route freshness, route-decision status, and SecretStore backend status; the
CLI text output prints the same summary as a single line, and the Web Probe
surfaces it in the visible summary area.
