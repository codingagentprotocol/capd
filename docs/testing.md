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

On macOS, repeated native SecretStore prompts or `macOS keychain status -128`
mean the OS denied or canceled Keychain access for the current process. Approve
the Keychain prompt and rerun the readiness check, or avoid native prompts for
local testing by restarting with `capd start --secret-backend file` and
re-importing accounts with `capd accounts --secret-backend file codex import`.

## Codex Account Smoke

Import at least one Codex `auth.json`, then run the local smoke check:

```bash
capd accounts codex import
capd accounts codex import --auth /tmp/acct-a/auth.json --auth /tmp/acct-b/auth.json
CAPD_CODEX_AUTH_PATHS="/tmp/acct-a/auth.json:/tmp/acct-b/auth.json" capd accounts codex import
CAPD_CODEX_AUTH_PATHS="C:\tmp\acct-a\auth.json;C:\tmp\acct-b\auth.json" capd accounts codex import
capd accounts codex smoke
capd accounts codex smoke --json
```

Repeat `--auth` for explicit multi-account imports. `CAPD_CODEX_AUTH_PATHS`
uses the OS path-list separator (`:` on macOS/Linux, `;` on Windows) and is
only used when `--auth` is not supplied. Prefer repeated `--auth` flags when
sharing commands across platforms.

For real quota validation:

```bash
capd accounts codex smoke --quota
```

`capd accounts codex quota all` refreshes accounts in stable account-id order.
If a later account fails, the command still prints safe `ok:false` partial JSON
with summaries for accounts refreshed before the failure, the failed account id,
and next steps; token material and backend debug fields stay redacted.

For multi-account routing readiness:

```bash
CAPD_SECRET_BACKEND=native capd doctor --prompt-free --json --fail --require-secret-backend native
CAPD_SECRET_BACKEND=native capd accounts --secret-backend native codex quota all --timeout 2m
CAPD_SECRET_BACKEND=native capd accounts --secret-backend native codex smoke --json --quota --require-multiple --require-fresh-quota --require-all-fresh-quota --require-secret-backend native --timeout 2m
CAPD_SECRET_BACKEND=native capd secretstore check --json --roundtrip --require-backend native --timeout 2m
CAPD_SECRET_BACKEND=native capd doctor --json --fail --verify-secretstore --require-secret-backend native --timeout 2m
CAPD_SECRET_BACKEND=native capd doctor --repair-plan --prompt-free --require-secret-backend native
make live-codex-repair-plan
make live-codex-repair-commands

# In another terminal, keep the daemon running for CAP/WebSocket checks:
capd start --secret-backend native

capd console --probe --require-secret-backend native
capd probe data --json --readiness --require-secret-backend native --timeout 2m --fail
capd probe evidence --manifest /tmp/capd-live-evidence/manifest.json --fail
curl -H "Authorization: Bearer $(cat ~/.capd/token)" http://127.0.0.1:7777/probe/data
curl -H "Authorization: Bearer $(cat ~/.capd/token)" "http://127.0.0.1:7777/probe/data?readiness=1&requireSecretBackend=native"
capd health --json --require-secret-backend native
capd accounts import --auth /tmp/acct-a/auth.json --auth /tmp/acct-b/auth.json
CAPD_CODEX_AUTH_PATHS="/tmp/acct-a/auth.json:/tmp/acct-b/auth.json" capd accounts import
CAPD_CODEX_AUTH_PATHS="C:\tmp\acct-a\auth.json;C:\tmp\acct-b\auth.json" capd accounts import
capd accounts check --json
capd accounts check --json --readiness
capd agents usage codex --account auto
capd agents route --account auto --require-fresh-quota --json
capd run --agent codex --account auto --require-fresh-quota "say ready"
```

The full console's ordinary diagnostic refresh and the browser probe's ordinary
`Refresh` path use `accounts/list` metadata plus route evidence and do not read
account SecretStore credentials, so they can be used for low-friction
diagnostics without repeated OS credential prompts. Their ordinary next steps
prefer `capd doctor --prompt-free`; use the console's `就绪门禁`, the probe's
`Readiness`, `--readiness`, or `--require-secret-backend native` when you
intentionally want the native SecretStore gate.
The full console also has a diagnostic package viewer for pasting live selftest
`manifest.json` or `summary.json` output and checking the recorded route, probe,
doctor, smoke, and SecretStore evidence artifact paths without reading local
files from the browser.
Paste the matching artifact JSON into the same viewer to get a compact QA report
for route policy, route candidates, quota freshness, and repair-plan evidence.
For automation, `capd probe evidence --manifest
/tmp/capd-live-evidence/manifest.json --fail` reads the saved manifest plus its
listed artifacts and exits non-zero when route policy, route candidates, fresh
quota evidence, backend, daemon mode, or a passed selftest status is missing.

After importing multiple accounts and starting the daemon in another terminal,
the same live preflight is available without sending a prompt:

```bash
make live-codex-preflight
```

For unattended live validation, let the repository start a temporary daemon,
wait for `/healthz`, run the same preflight, and stop only the daemon it
started:

```bash
make live-codex-selftest
LIVE_RUN_PROMPT=1 LIVE_PROMPT="say ready" make live-codex-selftest
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
`make live-codex-selftest` builds one temporary capd binary, starts the
temporary daemon with it when needed, and passes the same binary into
`live-codex-preflight` and the optional final prompt. Manual preflight defaults
to `go run ./cmd/capd`; pass `CAPD_BIN=./capd` to validate a specific build.
If preflight fails, the selftest prints a prompt-free readiness gap summary
first, then daemon health, safe Codex account metadata, fresh route gate JSON,
and smoke JSON. Set `LIVE_DIAGNOSE_SECRETSTORE=1` only when you want the
additional doctor, daemon-side accounts/check, and probe readiness diagnostics
that may touch native SecretStore prompts.
Set `CAPD_LIVE_SUMMARY=/tmp/capd-live-summary.json` to write a small machine-readable
selftest result with `summaryVersion`, `status`, `stage`, `checkedAt`,
`backend`, `host`, `port`, `daemonMode`, `logPath`, `bin`, and
`repairPlanPath` so CI or
long-running tasks can tell whether the failure happened in daemon startup,
preflight, or the final live prompt and collect the matching daemon log.
The file is updated as the run advances with `status:"running"` and the latest
reached `stage`, then overwritten with the final `passed` or `failed` result.
Set `CAPD_LIVE_REPAIR_PLAN=/tmp/capd-live-repair.json` to persist the
prompt-free doctor report, including the ordered `repairPlan`, whenever daemon
startup, backend alignment, preflight, or the optional live prompt fails.
Set `CAPD_LIVE_EVIDENCE_DIR=/tmp/capd-live-evidence` to persist safe JSON
evidence artifacts. On success the directory receives the final
`agents-route.json`, `probe-data-readiness.json`, and `doctor-prompt-free.json`
outputs, plus a `manifest.json` index. The summary JSON records the manifest and
primary evidence paths. Those files prove the live account count, fresh quota
gate, active `routePolicy`, route-candidate ordering, and SecretStore backend
without exposing tokens or reading shell logs. Successful selftest runs validate
that package with `capd probe evidence --manifest ... --fail` before reporting
`status:"passed"`. On
preflight failure, the same directory captures prompt-free failure diagnostics
such as `accounts-list.json`, `agents-route.json`, `probe-data-prompt-free.json`,
and `accounts-smoke.json`; enabling `LIVE_DIAGNOSE_SECRETSTORE=1` also captures
the heavier SecretStore-reading readiness files.

Without real Codex accounts, run the deterministic simulated gate:

```bash
make verify-codex-readiness-sim
```

It exercises multi-account quota refresh, conservative auto-route selection,
fresh-quota enforcement, concurrent quota refresh with route-candidate reads,
route-candidate reason evidence, concurrent CAP `accounts/quota all` plus fresh auto-route calls with secret-leak
guards, quota RawJSON redaction, invalid quota percent hardening, daemon-side
`accounts/check` readiness, doctor CAP/WebSocket account checks, WebSocket
disconnect/reconnect session continuity, Web probe readiness summaries, probe native SecretStore defaults,
SecretStore recovery next steps, `/healthz` backend gates, direct smoke route-candidate evidence, CLI shortcut
parameters, direct SecretStore JSON roundtrip, migration
readback-before-metadata-update safety, browser token cleanup documentation, and
secret-leak guards with local test backends.

`make live-codex-preflight` first prints safe account metadata and runs the
multi-account smoke gate before native SecretStore roundtrip prompts, so a
missing second account fails fast without unnecessary OS approval dialogs. It
then verifies the selected SecretStore backend, refreshes every Codex quota, and
runs `capd doctor --json --fail` against fresh local evidence before the
daemon/Web readiness chain. It also validates the tokenized Web probe URL with
the same SecretStore backend requirement before fetching `/probe/data`, and
prints the final auto-route gate as JSON so the log preserves sorted
`routeCandidates` evidence.
`make live-codex-readiness` runs that preflight before the final live prompt.
Override the prompt with
`LIVE_PROMPT="..." make live-codex-readiness`. Override the backend with
`LIVE_SECRET_BACKEND=file` only when intentionally testing the file SecretStore
path; the default live backend is `native`.
`make live-codex-selftest` is the same live gate for long tasks and release
checks that should not depend on a second terminal. It reuses an already healthy
daemon when one is listening on `CAPD_HOST`/`CAPD_PORT`; otherwise it starts a
temporary foreground daemon in the background, waits for health with the
requested SecretStore backend, and cleans up that temporary process on exit.
If the live preflight fails, the selftest prints a prompt-free readiness gap
summary, daemon health, safe `capd accounts codex list --json` metadata, and
fresh route JSON, prompt-free `/probe/data` evidence, and the multi-account
smoke gate; by default it does not run SecretStore-reading checks. Set
`LIVE_DIAGNOSE_SECRETSTORE=1` when you want failure diagnostics to
also run SecretStore-reading checks such as
`capd doctor --json --fail --verify-secretstore`, `capd accounts check --json
--readiness`, and authenticated `/probe/data` readiness.
If a daemon is already healthy on the target port but reports a different
SecretStore backend, the selftest fails immediately and asks you to restart
that daemon instead of trying to start a second process on the same port.
When doctor reports missing accounts and the daemon is running, prefer
`capd accounts import --auth ...` or
`CAPD_CODEX_AUTH_PATHS=... capd accounts import` so the import uses the same
CAP/WebSocket path as the Web Console. `capd accounts codex import` remains
available for direct local imports when the daemon is not running.

`capd secretstore check --json --roundtrip --require-backend native --timeout 2m` is the
smallest direct native SecretStore gate. It opens the active backend, writes,
reads, and deletes a diagnostic secret, and fails before account checks if the
daemon/live shell is using the wrong backend or an OS credential prompt stalls
native access. `capd doctor --prompt-free --json --fail
--require-secret-backend native` is the fastest preflight before the live chain
when you only need daemon, metadata, cached quota, and route evidence without
SecretStore prompts. `capd doctor --json --fail
--verify-secretstore --require-secret-backend native --timeout 2m`
is the deeper preflight before the live chain. It does not refresh quota or
read token material into the output; it reports daemon health, Codex CLI
availability, imported account count, cached quota freshness, auto-route
freshness, SecretStore backend, per-account SecretStore credential readability
with safe `secretState` categories such as `backend-mismatch`, `timeout`, or
`access-denied`,
an explicit SecretStore write/read/delete roundtrip, daemon-side CAP
`accounts/check` reachability, readiness issues, and concrete next steps. Its
JSON also includes the same `routeCandidates` ordering used by
`agents/route --account auto`, so live preflight evidence explains why one
account would be selected. The top-level `repairPlan` is the ordered autopilot
view: each entry has an `id`, copy/paste `command`, expected evidence, and
daemon/SecretStore requirements without token material or local runtime paths.
Doctor, `/probe/data`, and the Web Console consume the shared
`protocol.RepairStep` shape so automation can validate one remediation contract
instead of separate CLI and browser-only structures.
Use `capd doctor --repair-plan --prompt-free --require-secret-backend native`
when automation only needs the ordered repair commands and should avoid native
SecretStore credential prompts. Use `make live-codex-repair-plan` when the
automation should share `LIVE_SECRET_BACKEND` and `CAPD_BIN` with
`make live-codex-preflight`. Use `capd doctor --repair-commands --prompt-free`
or `make live-codex-repair-commands` when logs should contain one copy/paste
command per line without requiring `jq`. Use
`capd repair run --require-secret-backend native` to dry-run the same plan as an
approval-gated autopilot; add `--execute --yes` only when the log shows the
remaining steps are runnable. The runner skips placeholder auth paths, daemon
startup, shell `export` commands, and the final live preflight unless
`--include-final` is explicitly set. The Web Console and Probe classify the
same `repairPlan` entries as runnable or manual so browser-side diagnostics can
be compared directly with `capd repair run --json`; both paths expose
`execution.runnable` and `execution.reason` for machine checks.
The top-level `summary` object is the compact
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
`missing`), limiting quota-window evidence, sorted `routeCandidates`, safe
`routePolicy`, plus `secretBackend`; per-account rows include
`secretChecked`, `runtimeChecked`, `secretBackendOk`, `secretReadable`,
`secretState`, `quotaState`, `quotaFresh`, and `quotaCheckedAt` fields. Early
prompt-free failures leave the checked booleans false, distinguishing cached
metadata from a failed SecretStore or runtime check; text output mirrors those
safe secret/runtime/quota columns for terminal checks. Token material is never printed. Use
`--require-fresh-quota` to fail unless the auto-route decision is backed by a
fresh cached quota snapshot; use `--require-all-fresh-quota` to fail unless
every imported account has fresh cached quota. Use
`--require-secret-backend native` to fail unless smoke is reading credentials
from the OS secret backend.
When smoke fails before SecretStore reads, such as `--require-multiple` with
only one imported account or a requested SecretStore backend mismatch, JSON
output still includes safe cached account, quota, auto-route, and
`routeCandidates` evidence so live readiness logs show the real missing gate
without prompting for OS credentials. Its `nextSteps` also includes safe
parallel repairs when cached evidence proves them, such as refreshing stale
auto-route quota or rerunning with the account's SecretStore backend.
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
JSON form preserves safe partial evidence when a gate fails, including the
shared `repairPlan` with `execution.runnable` and `execution.reason` so it can
be compared directly with `/probe/data` and `capd repair run --json`.
The Web Console renders that repair plan from both the direct `accounts/check`
buttons and the `/probe/data` deep verification path.
If quota refresh fails midway, the partial evidence reflects any quota snapshots
successfully refreshed before the failing account, so readiness logs show which
accounts are already fresh and which account blocked the run.
It exits non-zero when too few accounts are imported, auto-route quota is stale
or missing, any checked account lacks fresh cached quota, or the daemon is not
using the expected native SecretStore backend. Override that backend with
`--require-secret-backend file` only when intentionally testing the file
backend.
Early preflight failures, such as SecretStore backend mismatch, still return
cached account, quota, `autoRoute`, and `routeCandidates` evidence without
reading SecretStore credentials or projecting runtimes.
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
auto-route freshness, route-decision status, and SecretStore backend status,
plus a `repairPlan` with runnable commands and expected evidence for Web/CI
autopilot flows. The CLI text output prints the same summary as a single line,
preserves route-candidate `secretBackend` enums when present, and renders the
repair plan as copy/paste commands, while the Web Probe surfaces the summary
and repair evidence in the visible area.
