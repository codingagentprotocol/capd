# capd reference

Complete parameter reference: every CLI flag, every protocol field, every
environment variable and file. For a guided tour, read the [README](../README.md).

## Liveness: knowing what's alive

| Question | How to ask |
|----------|------------|
| Is the daemon up? | `GET http://127.0.0.1:7777/healthz` → `ok`, or `?format=json` for safe daemon metadata (no auth needed) |
| Which agents are installed? | `capd agents list` / method `agents/list` — `available`, version, binary path per agent |
| Which sessions exist, which are alive? | `capd sessions` / method `session/list` — state `live` (in memory now), `stored` (revives on touch), `ended` |
| Is my connection still good? | server pings every 30 s; a missed pong (10 s) drops the connection — reconnect and `session/attach` |
| Did the agent engine die? | you receive `error` + `task.done {engineDied:true}` events; just use the session again, it revives |

## CLI

### `capd start` — run the daemon (foreground)

| Flag | Default | Meaning |
|------|---------|---------|
| `--host` | `127.0.0.1` | bind address; `0.0.0.0` for server deployments (put TLS in front) |
| `--port` | `7777` | listen port |
| `--origins` | _(empty)_ | extra browser origins allowed for WebSocket, comma-separated or repeated; localhost is always allowed |
| `--secret-backend` | `CAPD_SECRET_BACKEND`, then `file` | account SecretStore backend for the daemon (`file` or `native`) |

The daemon also serves the local web console at `/console/` and a compact data
probe at `/probe/`; both connect back to `/ws` with the daemon token, so opening
either page does not bypass CAP authentication. Page responses are `no-store`
and include CSP, referrer, permissions, and frame-deny headers because the token
may be supplied once in the page URL. The console and probe remove that token
from history, then authenticate the WebSocket with `Sec-WebSocket-Protocol:
capd.auth.<base64url-token>` instead of putting the token in the WebSocket URL.
For automation and simple web validation, `GET /probe/data` returns the same
safe daemon/account/route diagnostics as JSON and accepts only
`Authorization: Bearer <daemon-token>`, not query-string tokens.
The console exposes account import,
current-account selection,
runtime projection, selected or all-account quota refresh, safe account checks,
and readiness gates over the same CAP RPC methods used by CLI clients.
Use `capd console` to open the full console, or `capd console --probe` to open
the lightweight validation probe without printing the daemon token to the
terminal. The probe's Evidence JSON includes a `checks` array with
`name`, `ok`, `evidence`, and optional `nextStep` fields plus safe `health`
metadata (`version`, `protocolVersion`, and `secretBackend` when supported) so
browser-side readiness evidence mirrors `capd doctor --json`. The probe also
fetches `/probe/data` with an Authorization header so Web clients can validate
the HTTP diagnostics path without exposing the token in URLs, and its visible
summary includes the daemon's compact readiness `summary`. The full console
renders the same readiness concepts as visible pass/fail cards under the
diagnostic line, includes a compact readiness summary, can run `深度验证`
against the same header-authenticated `/probe/data` readiness endpoint, and shows the
account-aware `routeCandidates` returned by `agents/route` and `accounts/check`
so route choices can be audited from the browser without exposing token material
or local runtime paths.
When the native SecretStore gate is enabled, the console and probe distinguish
backend mismatches from deeper OS SecretStore verification and point the latter to
`capd doctor --json --fail --verify-secretstore --require-secret-backend native`.
If macOS reports `macOS keychain status -128`, the OS denied or canceled
Keychain access; approve the prompt, or run a local no-prompt file-backend test
with `capd start --secret-backend file` and re-import with
`capd accounts --secret-backend file codex import`.

### `capd health` — check the daemon

Checks the configured daemon `/healthz` endpoint and prints `ok`, or
`{"ok":true,"addr":"...","daemon":"capd","version":"...","protocolVersion":"...","secretBackend":"..."}` with
`--json` when the daemon supports safe health metadata. The JSON path falls back
to `{"ok":true,"addr":"..."}` for older daemons.
`--require-secret-backend <file|native>` fails unless the daemon reports that
active backend, and also fails against older daemons that cannot report
`secretBackend`. Failures point to `capd start`, making it useful before
daemon-side account readiness checks.

### `capd doctor` — local readiness preflight

Runs a read-only readiness preflight across daemon health, local agent
discovery, imported Codex account count, per-account SecretStore credential
readability, cached quota freshness, auto-route freshness, and the active
SecretStore backend. Use
`--require-secret-backend native` to turn native SecretStore into an explicit
readiness issue. When the daemon is healthy, doctor also calls daemon-side
`accounts/check` over CAP/WebSocket to confirm the same account evidence that
the Web Console uses. Add `--verify-secretstore` when you want doctor to write,
read, and delete a diagnostic secret in the active SecretStore backend; this is
useful before native-backend live runs. Text output returns a non-zero exit code
when readiness issues are found. `--json` prints safe machine-readable evidence
and next steps without token material or local secret paths; add `--fail` when
JSON output should also return non-zero on readiness issues. Auto-route evidence
includes the selected account id, quota state, freshness, primary quota usage,
routing score, checked time, and the same human-readable reason used by routing previews. The
`codex.accounts` JSON array lists safe per-account credential and quota evidence
(id, email, current marker, plan, `secretBackendOk`, `secretReadable`,
`secretState`, quota state, freshness, primary usage, checked time) and never
includes SecretStore refs, token material, runtime paths, or raw auth JSON.
`secretState` is a safe enum such as `readable`, `backend-mismatch`, `missing`,
`timeout`, `access-denied`, `malformed-ref`, or `unreadable`; `codex.secretStates` aggregates
those categories by count for CI and Web clients.
The top-level `summary` object gives CI and Web clients a compact readiness
view: required/missing account counts, readable/unreadable SecretStore credential
counts, fresh/stale/missing quota counts, auto-route freshness, route candidate
count, daemon CAP status, SecretStore backend match, and optional SecretStore
roundtrip status.
The top-level `checks` array is a stable readiness checklist with
`name`, `ok`, `evidence`, and optional `nextStep` fields for daemon health,
Codex CLI availability, SecretStore backend, per-account SecretStore credential
readability, multi-account import, quota freshness, and auto-route freshness.
When accounts are missing and the daemon is healthy, doctor next steps point to
`capd accounts import` so the fix exercises the same CAP/WebSocket import path
as web clients; when a second account is needed, the next step points to
`make live-codex-preflight` before the final live prompt. The local
`capd accounts codex import` remains a fallback when the daemon is not running.
When quota or auto-route freshness is missing, doctor next steps point to
`capd accounts check --json --readiness` so the same daemon-side
refresh-and-verify gate used by web clients can confirm the fix while returning
safe partial evidence on failure.

### `capd run <prompt>` — send one task and stream it

| Flag | Default | Meaning |
|------|---------|---------|
| `--agent` | `codex` | agent id from `capd agents list` |
| `--cwd` | current directory | project directory the agent works in |
| `--session` | _(new session)_ | continue an existing session id (multi-turn; survives restarts) |
| `--account` | — | imported account id for a new session; currently Codex only |
| `--require-fresh-quota` | off | with `--account auto`, fail unless the selected Codex account has fresh cached quota |
| `--permission` | `default` | `default` (read-only sandbox + approvals) · `acceptEdits` (workspace-write) · `full` (no prompts) |
| `--model` | agent default | agent-native model id, e.g. `gpt-5.3-codex` |
| `--effort` | agent default | reasoning effort where supported (codex: `minimal` `low` `medium` `high` `xhigh`) |
| `--image` | — | image file(s) to attach; repeatable (agents with image support) |
| `--json` | off | raw event JSON instead of formatted output |

Interactive: approval requests pause the stream and ask
`[y]es / [a]lways / [N]o` (Enter = deny). Exit prints the session id for
follow-ups.

When `--account auto --require-fresh-quota` fails because the selected Codex
account lacks fresh cached quota, `capd run` prints any safe daemon-provided
`accountRoute` and `routeCandidates` evidence, then prints recovery commands
that point back to the shared readiness path:
`capd accounts check --readiness` to refresh and verify daemon-side quota
evidence, or
`capd agents route --account auto --require-fresh-quota` to preview the route
gate before sending another prompt.

### `capd watch <session-id>` — attach without sending (long tasks)

| Flag | Default | Meaning |
|------|---------|---------|
| `--from` | `0` | replay history from this sequence number |
| `--tail` | off | skip replay, live output only |
| `--json` | off | raw event JSON |

Long-task pattern: start with `capd run`, Ctrl-C any time (the turn keeps
running in the daemon), find it with `capd sessions`, re-join with
`capd watch`. Exits when the session ends; Ctrl-C to stop watching.

### `capd agents` — discovery and account data

| Command | Output |
|---------|--------|
| `capd health [--json] [--require-secret-backend <file\|native>]` | prints `ok` when the configured daemon is serving `/healthz`; `--json` includes `ok`, `addr`, and daemon metadata such as version, protocol version, and active SecretStore backend when supported; `--require-secret-backend` fails early when the daemon was started with the wrong backend |
| `capd secretstore check [--json] [--roundtrip] [--secret-backend <file\|native>] [--require-backend <file\|native>] [--timeout 2m]` | opens the selected SecretStore backend and prints safe readiness evidence. `--roundtrip` writes, reads, and deletes a diagnostic secret; `--require-backend` fails when the active backend differs, making this the smallest direct native SecretStore gate before live account checks. `--timeout` bounds OS credential backend waits, including native prompts. |
| `capd console [--probe] [--url] [--require-secret-backend <file\|native>]` | opens the local web console, or the compact validation probe with `--probe`, after checking daemon health. By default it passes the daemon token to the browser without printing it; `--url` prints the tokenized URL only when explicitly requested. `--require-secret-backend` preloads the page's readiness gate with the selected SecretStore backend so browser checks match CLI readiness runs. |
| `capd probe data [--json] [--readiness] [--fail] [--require-secret-backend <file\|native>] [--timeout 2m]` | fetches `/probe/data` with `Authorization: Bearer <daemon-token>` and prints safe diagnostics for automation. Text output includes a compact readiness summary with route candidate count, actual/required SecretStore backend evidence, and server JSON errors when available. `--readiness` requests the stronger readiness view and defaults the daemon request to `requireSecretBackend=native`; use `--require-secret-backend file` only for intentional file-backend tests. `--timeout` bounds HTTP waits, and `--fail` exits non-zero when the probe reports `ok=false` or an HTTP error status. |
| `GET /probe/data` | authenticated HTTP diagnostics endpoint for Web clients and smoke tests. Requires `Authorization: Bearer <daemon-token>`, returns safe JSON health, a compact `summary`, `accounts/check`, `agents/route`, `routeCandidates`, and pass/fail `checks` for account credentials, account runtime projection, quota freshness, route decisions, and SecretStore backend state; `?readiness=1` defaults to `requireSecretBackend=native` for the stronger live readiness view, while `?readiness=1&requireSecretBackend=file` is reserved for intentional file-backend tests. `requireSecretBackend` accepts only `file` or `native`; unknown values fail fast with HTTP 400 before quota or route checks run. The top-level `summary` reuses `accounts/check.summary` for account, quota, auto-route, and SecretStore evidence, then adds route-decision status for the full Web diagnostics path. The handler also bounds server-side work: ordinary probes get 12s and readiness probes get 2m. |
| `capd doctor [--json] [--fail] [--verify-secretstore] [--require-secret-backend <file\|native>] [--timeout 2m]` | local readiness preflight for daemon health, Codex CLI availability, imported account count, per-account SecretStore credential readability, quota freshness, auto-route freshness, daemon-side CAP account evidence, and SecretStore backend; `--verify-secretstore` performs an explicit write/read/delete diagnostic roundtrip; `--timeout` bounds native SecretStore and account checks; text mode fails when issues are found, and `--fail` makes JSON mode fail too |
| `capd agents list` | table: id, available/not installed, version, binary path |
| `capd agents route [--account <id\|auto>] [--capability name] [--require-fresh-quota] [--json]` | preview local routing without starting a session; with `--account auto`, shows the Codex account selected by conservative quota scoring. JSON includes `routeCandidates`, sorted by the same account-aware routing score. `--require-fresh-quota` fails unless that auto selection is backed by fresh cached quota; failing text output includes the selected route and route-candidate quota evidence so users can see stale or missing quota before refreshing |
| `capd agents usage <id>` | account snapshot JSON: plan, 5h/weekly window used %, reset timestamps, credits (codex) |
| `capd agents usage codex --account <id\|auto>` | usage for an imported Codex account, or the account selected by conservative quota scoring with `auto`; also refreshes the local quota snapshot |

### `capd accounts` — local account control plane

Common flag: `--secret-backend <file|native>` selects where account token
material is read/written for the command. Empty uses `CAPD_SECRET_BACKEND`, then
the file backend.

| Command | Meaning |
|---------|---------|
| `capd accounts list [--json]` | List imported account metadata across all providers; provider-scoped current accounts are marked with `*`; quota state is reported as `fresh`, `stale`, or `missing`. |
| `capd accounts import [--provider codex] [--auth path ...] [--json]` | Requires a running daemon (`capd start`). Imports one or more Codex `auth.json` files through daemon `accounts/import`, matching the CAP/WebSocket path used by web clients. Repeat `--auth` to import multiple explicit paths; without `--auth`, the daemon uses its default `~/.codex/auth.json`. |
| `capd accounts check [--provider codex] [--json] [--readiness] [--refresh-quota] [--require-multiple] [--require-fresh-quota] [--require-all-fresh-quota] [--require-secret-backend <file\|native>] [--timeout 2m]` | Requires a running daemon (`capd start`). Optionally refresh every imported Codex quota through daemon `accounts/quota`, then call the daemon's `accounts/check` RPC and print safe smoke evidence without token material or runtime paths. JSON includes a compact `summary` and `routeCandidates` when imported accounts are available, using the same ordering as `agents/route --account auto`; failing JSON calls also print structured `ok:false` errors and safe `nextSteps`, including macOS Keychain `access-denied` recovery when native SecretStore access is canceled. Use `capd accounts codex smoke` for direct local checks that do not need the daemon. `--readiness` is the recommended daemon-side gate for live Codex work: it enables quota refresh, multiple-account, fresh auto-route, all-fresh quota, and native SecretStore requirements by default. `--timeout` bounds daemon-side waits so live preflight does not hang on remote quota refresh. `--require-secret-backend` accepts only `file` or `native` and can override the readiness backend for intentional file-backend tests. |

### `capd accounts codex` — local Codex account control plane

| Command | Meaning |
|---------|---------|
| `capd accounts codex import [--auth path ...]` | Import one or more Codex `auth.json` files into capd. Repeat `--auth` to import explicit paths in one command. Without `--auth`, `CAPD_CODEX_AUTH_PATHS` can provide an OS path-list of auth files (`:` on macOS/Linux, `;` on Windows); otherwise it defaults to `~/.codex/auth.json`. |
| `capd accounts codex list [--json]` | List imported Codex account metadata; the current account is marked with `*`; `secretBackend` shows the safe backend enum (`file`, `native`, or `malformed`) without the SecretStore ref id; quota state is reported as `fresh`, `stale`, or `missing`. `--json` prints safe account/quota metadata for live readiness audits without SecretStore refs or token material. |
| `capd accounts codex current [account-id]` | Show or set the current Codex account. |
| `capd accounts codex project [account-id]` | Create or refresh a capd-managed per-account `CODEX_HOME`; prints the path. |
| `capd accounts codex remove <account-id>` | Remove an imported Codex account, its cached quota/session bindings, current-account state, SecretStore token bundle, and capd-managed `CODEX_HOME` projection. |
| `capd accounts codex migrate-secrets [account-id\|all] [--from file\|native] [--to file\|native] [--dry-run] [--delete-source] [--json] [--timeout 2m]` | Move imported Codex account credentials between SecretStore backends and update safe account metadata refs without printing token material. Defaults to `--from file --to native` for upgrading older imports to the OS SecretStore. The target secret is read back before account metadata is updated; if target readback fails, capd removes the attempted target secret, keeps the source ref, and reports safe partial evidence. By default it keeps the source secret as a rollback path; add `--delete-source` only after native readiness passes. `--dry-run` reports planned account moves without writing target secrets. |
| `capd accounts codex quota [account-id\|auto\|all] [--raw] [--timeout 2m]` | Fetch ChatGPT backend quota for imported Codex accounts and update local quota snapshots. `auto` uses the same conservative quota scoring rule as account-aware routing. `all` refreshes every imported Codex account and prints safe summaries. Defaults to a safe summary; `--raw` prints backend usage JSON for debugging and is only supported for a single account. `--timeout` bounds SecretStore reads and remote quota waits. |
| `capd accounts codex smoke [--json] [--quota] [--require-fresh-quota] [--require-all-fresh-quota] [--require-secret-backend <file\|native>] [--timeout 2m]` | Verify imported accounts, SecretStore readability, per-account projection, auth file permissions, auto-route account selection, and optionally quota refresh without printing token material. JSON includes `autoRoute.accountId`, `autoRoute.quotaState` as `fresh`, `stale`, or `missing`, and `routeCandidates` sorted by the same account-aware routing score, plus per-account `secretBackendOk`, `secretReadable`, `quotaState`, `quotaFresh`, and `quotaCheckedAt`; with `--json`, failing gates still print safe partial evidence with `ok:false`, `issues`, and `nextSteps`. Text output prints the same auto-route and route-candidate quota evidence for quick terminal checks. `--timeout` bounds SecretStore reads, runtime projection, and optional quota refresh. `--require-fresh-quota` fails unless auto-route selection is backed by fresh cached quota; `--require-all-fresh-quota` fails unless every imported account has fresh cached quota; `--require-secret-backend` fails unless the active SecretStore backend matches. |

The import stores token material in `~/.capd/secrets/codex/*.json` with mode
0600. SQLite stores only account metadata plus a `secret_ref`; access tokens,
refresh tokens, ID tokens, and API keys are intentionally kept out of the
database, protocol responses, and logs. Account operations fail closed when an
account's `secret_ref` backend does not match the active SecretStore backend,
before token material is read.

Codex-managed ChatGPT OAuth refresh is handled by Codex itself inside each
projected `CODEX_HOME`. Before capd rewrites a projection, it checks whether
the projected `auth.json` has a newer `last_refresh` than SecretStore and, if
so, syncs that refreshed token bundle back into SecretStore. Projection is
serialized per account so concurrent sessions cannot overwrite each other's
refreshed token file.

### `capd sessions` — session inventory

Table: session id, agent, state (`live`/`stored`/`ended`), created, project
directory. Newest first, up to 100.

### `capd service` — run as a system service

`install` · `uninstall` · `start` · `stop` · `restart` · `status` —
user-level launchd/systemd/Windows SCM unit running `capd start`; starts on
boot, restarts on crash, never root. `capd service install --secret-backend
native` persists the daemon backend as `capd start --secret-backend native`,
which is the recommended persistent setup for live multi-account Codex checks.
If `--secret-backend` is omitted, `capd service install` also honors
`CAPD_SECRET_BACKEND` at install time and writes the selected backend into the
service arguments so launchd/systemd/Windows SCM do not need to inherit shell
environment variables later.

## Environment variables

| Variable | Meaning |
|----------|---------|
| `CAPD_HOST` | same as `--host` |
| `CAPD_PORT` | same as `--port` |
| `CAPD_ORIGINS` | comma-separated extra WebSocket origins |
| `CAPD_SECRET_BACKEND` | secret storage backend; default `file`. `native` uses the OS secret backend where implemented; macOS stores bundles in Keychain, Windows uses Credential Manager, Linux uses Secret Service via `secret-tool`, and unsupported platforms fail closed |

Precedence: flags > environment > defaults.

## Files

| Path | Contents |
|------|----------|
| `~/.capd/token` | connection token, 0600, generated on first run |
| `~/.capd/capd.db` | SQLite: session identities + full event log |
| `~/.capd/accounts.db` | SQLite: account metadata, current account, quota snapshots, session-account binding |
| `~/.capd/secrets/codex/*.json` | file secret backend for imported Codex token material, 0600 |
| `~/.capd/runtimes/codex/<account-id>/` | capd-managed per-account `CODEX_HOME` projection |

## Protocol reference

Transport: `ws://HOST:PORT/ws`, JSON-RPC 2.0. Browser clients should
authenticate with `Sec-WebSocket-Protocol: capd.auth.<base64url-token>`
(base64url without padding). Non-browser clients may use `Authorization: Bearer
TOKEN`; `?token=TOKEN` remains supported for backward compatibility with older
local clients. The bundled Console and Probe pages accept that query token only
as a bootstrap path: after reading it they remove `token` from the visible URL
with `history.replaceState`, send WebSocket auth via the `capd.auth.*`
subprotocol, and do not persist daemon tokens in localStorage or sessionStorage.
All session activity arrives as `event` notifications.

### `initialize` — must be first

```json
{"protocolVersion": "0.1", "client": {"name": "my-app", "version": "1.0"}}
```
→ `{"protocolVersion": "0.1", "daemon": {"name": "capd", "version": "0.1.0"}}`
Mismatched versions are rejected with code `-32005`.

### `agents/list`

No params. → `{"agents": [{"id", "name", "bin", "version", "available", "capabilities"}]}`

`capabilities` is daemon-known behavior clients can use for routing and UI:
`model`, `effort`, `streaming`, `approvals`, `steer`, `fork`, `rollback`,
`review`, `images`, `usage`, `resume`.

### `agents/route`

Ask capd to pick an installed agent. Params mirror route signals:
`{"prompt", "attachments", "accountId", "model", "effort", "capabilities", "prefer", "requireFreshQuota"}`.

→ `{"agent": {...}, "accountId": "codex-acct", "accountRoute": {"accountId": "codex-acct", "quotaState": "fresh", "fresh": true, "primaryUsedPercent": 12, "score": 12, "checkedAt": 1781170000}, "routeCandidates": [{"accountId": "codex-acct", "quotaState": "fresh", "fresh": true, "primaryUsedPercent": 12, "score": 12, "checkedAt": 1781170000}], "reason": "matched capabilities: effort, review"}`

When `accountId` is present, routing is account-aware and currently selects
Codex only, because imported account runtimes are Codex-specific. Use
`accountId:"auto"` to choose an imported Codex account by conservative quota
scoring: fresh cached primary quota uses the actual usage percent, while missing
quota or rows older than 30 minutes receive a conservative unknown score until
`accounts/quota` or `agents/usage` refreshes them. Set `requireFreshQuota:true`
with `accountId:"auto"` to fail instead of routing on missing or stale quota;
that fresh-quota error includes safe `data.accountRoute` and
`data.routeCandidates` evidence so clients can show the stale or missing quota
state without exposing token material.
When account routing is in play, `accountRoute` reports the selected
`accountId`, score, `quotaState` (`fresh`, `stale`, or `missing`), freshness,
optional primary usage percent, and optional checked timestamp without exposing
token material. `routeCandidates` contains the same safe evidence for every
imported candidate account, sorted by the same conservative score used for
selection; when routing succeeds, `accountRoute` should match the first
candidate.

### `agents/usage`

`{"agentId": "codex", "accountId": "codex-acct"}` → `{"agentId", "usage": {...}}` — agent-specific
shape; codex: `rateLimits.primary/secondary {usedPercent, windowDurationMins,
resetsAt}`, `planType`, `credits`, plus per-model buckets in
`rateLimitsByLimitId`.

### `accounts/list`

`{"provider": "codex"}` → `{"currentAccountId", "accounts": [...]}`.
Omit `provider` to list imported accounts across all providers; in that case
`currentAccountId` is omitted because current account is provider-scoped.

Returns imported account metadata and cached quota snapshots only. Quota
snapshots include server-classified `quotaState` (`fresh`, `stale`, or
`missing`) so clients do not have to duplicate freshness rules. It never returns
`secret_ref`, access tokens, refresh tokens, ID tokens, API keys, or raw quota
JSON.

### `accounts/import`

`{"provider": "codex", "authPath": "/path/to/auth.json"}` or
`{"provider": "codex", "authPaths": ["/tmp/a/auth.json", "/tmp/b/auth.json"]}` →
`{"currentAccountId", "importedAccounts": 2, "account": {...}, "accounts": [{...}]}`.

Imports one or more local Codex `auth.json` files from the daemon host into
capd. `authPaths` imports multiple explicit paths and takes precedence over
`authPath`. Omit both to use the daemon host default `~/.codex/auth.json`.
The response returns the same safe account summary shape as `accounts/list`;
`account` is the last imported account for older clients, while `accounts`
contains every account imported by this call. `importedAccounts` is the
provider's total imported account count after the call, so clients can guide the
user toward importing a second account or running readiness. It never returns
token material, `secret_ref`, raw auth JSON, or auth file paths.

### `accounts/current`

Read or set the provider-scoped current account without exposing secrets.

`{"provider": "codex"}` → `{"currentAccountId", "account": {...}}`.

`{"provider": "codex", "accountId": "codex-acct"}` sets the current Codex
account, then returns the same safe account summary shape as `accounts/list`.

### `accounts/project`

`{"provider": "codex", "accountId": "codex-acct"}` →
`{"accountId", "runtimeReady", "authJsonPrivate", "projectionMarkerOk"}`.

Creates or refreshes the capd-managed per-account `CODEX_HOME` projection and
verifies private `auth.json` and marker permissions. Omit `accountId` to use the
current Codex account. The response never returns token material, `secret_ref`,
or local filesystem paths.

### `accounts/check`

`{"provider": "codex", "refreshQuota": true, "requireMultiple": true, "requireFreshQuota": true, "requireAllFreshQuota": true, "requireSecretBackend": "native"}` →
`{"provider", "currentAccountId", "secretBackend", "checkedAccounts", "quotaRefreshed", "summary", "autoRoute", "routeCandidates", "accounts"}`.

Runs a safe local smoke check for imported Codex accounts: verifies SecretStore
backend matching, credential readability, per-account runtime projection,
private `auth.json`, projection marker integrity, cached quota freshness, and
auto-route evidence including the selected `autoRoute.accountId`. By default it
reads cached quota only; set `refreshQuota:true` to refresh every imported Codex
account first inside the daemon. The `require*` fields turn the same RPC into a
failing readiness gate for multi-account checks, fresh auto-route quota, fresh
quota on every checked account, and a specific SecretStore backend. The
response-level `summary` is the shared compact readiness view for CLI, Web,
Probe, and automation clients. It includes `ready`, account counts, quota
fresh/stale/missing counts, selected auto-route freshness, route-candidate
count, active and required SecretStore backend, and whether quota was refreshed
inside the call.
`capd accounts check --readiness` is CLI shorthand for setting
`refreshQuota:true`, `requireMultiple:true`, `requireFreshQuota:true`,
`requireAllFreshQuota:true`, and `requireSecretBackend:"native"` unless a
different `--require-secret-backend` is supplied.
With `--json`, a failing readiness or smoke gate still exits non-zero but prints
`{"ok":false,"error":{...},"data":{...}}` when the daemon returned safe partial
evidence in JSON-RPC error `data`. The partial evidence follows the same
redaction contract as a successful `accounts/check` response, including
`summary` when the daemon accumulated partial evidence.
`quotaRefreshed:true` means the returned evidence follows a successful quota
refresh in this same call. The response never returns token material,
`secret_ref`, raw auth JSON, or local filesystem paths.
When imported accounts are available, `routeCandidates` is included with the
same ordering and redaction contract as `agents/route`, so Web clients can show
why `autoRoute` was selected without making a second route call.
When a readiness gate fails after account metadata is loaded, the JSON-RPC
error `data` may contain the same safe `accounts/check` evidence accumulated so
far. Clients should treat it as partial evidence for diagnostics, not as a
successful readiness result. If remote quota refresh fails before per-account
runtime checks complete, partial evidence may still include cached
`routeCandidates` and `summary` so clients can show the pre-refresh account
routing and readiness state.

### `accounts/quota`

`{"provider": "codex", "accountId": "codex-acct"}` → `{"account": {...}}`.

`{"provider": "codex", "accountId": "all"}` → `{"accounts": [...]}`.

Refreshes one imported Codex account through the ChatGPT backend quota endpoint,
updates the local quota snapshot, and returns the same safe account summary
shape as `accounts/list`. Omit `accountId` to refresh the current Codex account.
Use `"accountId":"auto"` to refresh the account selected by the same
conservative quota scoring rule used by account-aware routing. Use
`"accountId":"all"` to refresh every imported Codex account in one daemon-side
operation; if one account fails, the RPC fails with that account id in the error
message instead of returning partial readiness evidence.
The response never returns token material, `secret_ref`, or raw backend JSON.

### `accounts/remove`

`{"provider": "codex", "accountId": "codex-acct"}` →
`{"accountId", "runtimeRemoved", "credentialRemoved", "currentAccountId", "remainingAccounts"}`.

Removes the imported account metadata, cached quota, session bindings,
SecretStore token bundle, and capd-managed `CODEX_HOME` projection. Runtime
removal fails closed unless `.capd_projection.json` proves the directory is
owned by capd for the requested Codex account. The response never returns token
material, `secret_ref`, or filesystem paths.

### `session/create`

| Field | Type | Default | Meaning |
|-------|------|---------|---------|
| `agentId` | string | required | agent to drive, or `auto` to route |
| `accountId` | string | — | imported account id, or `auto` to choose by conservative Codex quota scoring; currently supported for Codex sessions |
| `requireFreshQuota` | bool | `false` | with `accountId:"auto"`, fail unless the selected Codex account has fresh cached quota |
| `cwd` | string | user home | project directory; must exist |
| `permissionMode` | string | `""` (default) | `acceptEdits` · `full`; `full` is rejected at filesystem root |
| `model` | string | agent default | agent-native model id |
| `effort` | string | agent default | reasoning effort (codex) |
| `resume` | string | — | agent-native session id to resume |

→ `{"sessionId": "s_...", "accountId": "codex-..."}`; `accountId` is present
when the created session is bound to an imported account, including an
`accountId:"auto"` selection. The connection is auto-subscribed to its events.

`task/send` rejects empty work items, non-image attachments, non-absolute local
attachment paths, non-HTTP(S) attachment URLs, and more than 16 attachments.

### `session/list`

No params. → `{"sessions": [{"sessionId", "agentId", "accountId", "cwd", "state", "createdAt"}]}`
with `state` ∈ `live` | `stored` | `ended`. `accountId` is present only for
sessions created with an imported account; it is a safe local account id, never
token material.

### `session/attach`

`{"sessionId", "fromSeq": 0}` → `{"sessionId", "nextSeq"}` — replays
buffered events with `seq >= fromSeq` as notifications, then follows live.
Use the last `seq` you saw + 1 to resume without duplicates; use a huge
`fromSeq` for live-tail only. Touching a `stored` session revives it.

### `session/history`

| Field | Type | Default | Meaning |
|-------|------|---------|---------|
| `sessionId` | string | required | which session |
| `fromSeq` | uint | `0` | first sequence number to return |
| `limit` | int | 500 (max 5000) | page size |

-> `{"sessionId", "events": [...], "nextSeq"}` — a synchronous pull of past
events, no subscription, no session revival. Page forward by passing
`nextSeq` back as `fromSeq`; an empty page means you are caught up.

### `session/fork`

`{"sessionId"}` → `{"sessionId": "<new>"}` — an independent session sharing
conversation history up to this point (agents with fork support). If the parent
session was bound to an imported account, the fork inherits that safe local
`accountId` so `session/list` remains account-auditable.

### `session/rollback`

`{"sessionId", "numTurns": 1}` — drop the last N turns (`numTurns >= 1`).

### `session/close`

`{"sessionId"}` — permanent; the session shows as `ended` afterwards.

### `task/send`

| Field | Type | Meaning |
|-------|------|---------|
| `sessionId` | string | target session |
| `prompt` | string | the task |
| `attachments` | array | optional; `{"type":"image","path":"/abs/path"}` (daemon-local) or `{"type":"image","url":"https://..."}` |

Returns `{"ok":true}` immediately; results stream as events. One turn at a
time per session — a second send while running errors (`task/steer` instead).

### `task/steer`

`{"sessionId", "prompt"}` — inject guidance into the RUNNING turn without
interrupting it (agents with steer support).

### `task/cancel`

`{"sessionId"}` — interrupt the running turn; the session stays usable.

### `task/review`

| Field | Values |
|-------|--------|
| `sessionId` | target session |
| `target.type` | `uncommitted` (default) · `branch` · `commit` |
| `target.branch` | base branch, for `branch` |
| `target.commit` | sha, for `commit` |

Starts a code-review turn; findings stream as ordinary events.

### `task/reviewMulti`

Starts one reviewer session per requested agent and subscribes the caller to
all reviewer event streams. With no `agentIds`, capd selects every available
agent whose capabilities include `review`; `agentIds:["auto"]` routes to one
review-capable agent.

`{"target": {"type": "branch", "branch": "main"}, "agentIds": ["auto"], "cwd": "/repo"}`
→ `{"reviews": [{"agentId": "codex", "sessionId": "s_..."}]}`

### `approval/reply`

`{"sessionId", "approvalId", "decision"}` with decision `approve` ·
`approveAlways` (stop asking for this kind this session) · `deny`.
`approvalId` comes from the `approval.needed` event.

### Events

Each: `{"sessionId", "seq", "type", "data"}` — `seq` is per-session,
monotonic, gap-free.

| Type | Data |
|------|------|
| `session.started` | `nativeSessionId`, agent-specific extras (`model`, `cwd`, `forkedFrom`) |
| `session.ended` | — (terminal) |
| `output.text` | `text`; `delta:true` for streaming chunks, `final:true` closes a delta run (`itemId` correlates) |
| `output.reasoning` | same shape as output.text |
| `tool.use` | `kind` (`shell`, `fileChange`, ...), `command`, raw `item` |
| `tool.result` | `output`, `exitCode`, `delta:true` for live command output |
| `approval.needed` | `approvalId`, `kind` (`command`/`fileChange`/`permissions`), `command`, `cwd`, `reason` |
| `usage.updated` | agent-pushed rate-limit snapshot |
| `task.done` | `ok`, `result` (final agent text, where known), `usage` (tokens), `costUSD` where known, `canceled`/`engineDied` flags |
| `error` | `message` |

### Error codes

| Code | Meaning |
|------|---------|
| `-32700 / -32600 / -32601 / -32602 / -32603` | JSON-RPC standard: parse / request / method / params / internal |
| `-32000` | agent not found |
| `-32001` | agent unavailable (not installed, engine failed, discovery-only) |
| `-32002` | session not found |
| `-32003` | session ended |
| `-32004` | unauthorized |
| `-32005` | protocol version unsupported |

## Long-task playbook

1. `capd run --permission acceptEdits "migrate all tests to testify"` — the
   turn belongs to the session, not your terminal.
2. Ctrl-C, close the laptop lid, whatever. The daemon keeps going
   (`capd service install` recommended).
3. `capd sessions` → find the id; `capd watch s_xxx` → replay + follow.
4. From a web client: `session/attach {fromSeq}` resumes exactly where you
   left off; `task.done` tells you it finished, with token usage.
5. Need to redirect mid-flight? `task/steer`. Abort? `task/cancel`.
