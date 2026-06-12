# capd reference

Complete parameter reference: every CLI flag, every protocol field, every
environment variable and file. For a guided tour, read the [README](../README.md).

## Liveness: knowing what's alive

| Question | How to ask |
|----------|------------|
| Is the daemon up? | `GET http://127.0.0.1:7777/healthz` → `ok` (no auth needed) |
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

The daemon also serves the local web console at `/console/`; it still connects
back to `/ws` with the daemon token, so opening the page does not bypass CAP
authentication.

### `capd run <prompt>` — send one task and stream it

| Flag | Default | Meaning |
|------|---------|---------|
| `--agent` | `codex` | agent id from `capd agents list` |
| `--cwd` | current directory | project directory the agent works in |
| `--session` | _(new session)_ | continue an existing session id (multi-turn; survives restarts) |
| `--account` | — | imported account id for a new session; currently Codex only |
| `--permission` | `default` | `default` (read-only sandbox + approvals) · `acceptEdits` (workspace-write) · `full` (no prompts) |
| `--model` | agent default | agent-native model id, e.g. `gpt-5.3-codex` |
| `--effort` | agent default | reasoning effort where supported (codex: `minimal` `low` `medium` `high` `xhigh`) |
| `--image` | — | image file(s) to attach; repeatable (agents with image support) |
| `--json` | off | raw event JSON instead of formatted output |

Interactive: approval requests pause the stream and ask
`[y]es / [a]lways / [N]o` (Enter = deny). Exit prints the session id for
follow-ups.

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
| `capd agents list` | table: id, available/not installed, version, binary path |
| `capd agents route [--account <id\|auto>] [--capability name] [--require-fresh-quota] [--json]` | preview local routing without starting a session; with `--account auto`, shows the Codex account selected from cached quota. `--require-fresh-quota` fails unless that auto selection is backed by fresh cached quota |
| `capd agents usage <id>` | account snapshot JSON: plan, 5h/weekly window used %, reset timestamps, credits (codex) |
| `capd agents usage codex --account <id\|auto>` | usage for an imported Codex account, or the lowest fresh cached-quota Codex account with `auto`; also refreshes the local quota snapshot |

### `capd accounts` — local account control plane

Common flag: `--secret-backend <file|native>` selects where account token
material is read/written for the command. Empty uses `CAPD_SECRET_BACKEND`, then
the file backend.

| Command | Meaning |
|---------|---------|
| `capd accounts list [--json]` | List imported account metadata across all providers; provider-scoped current accounts are marked with `*`. |

### `capd accounts codex` — local Codex account control plane

| Command | Meaning |
|---------|---------|
| `capd accounts codex import [--auth path]` | Import a Codex `auth.json` into capd. Defaults to `~/.codex/auth.json`. |
| `capd accounts codex list` | List imported Codex account metadata; the current account is marked with `*`. |
| `capd accounts codex current [account-id]` | Show or set the current Codex account. |
| `capd accounts codex project [account-id]` | Create or refresh a capd-managed per-account `CODEX_HOME`; prints the path. |
| `capd accounts codex quota [account-id\|auto] [--raw]` | Fetch ChatGPT backend quota for an imported Codex account and update the local quota snapshot. `auto` selects the Codex account with the lowest fresh cached primary quota. Defaults to a safe summary; `--raw` prints backend usage JSON for debugging. |
| `capd accounts codex smoke [--json] [--quota] [--require-fresh-quota] [--require-secret-backend <file\|native>]` | Verify imported accounts, SecretStore readability, per-account projection, auth file permissions, auto-route account selection, and optionally quota refresh without printing token material. `--require-fresh-quota` fails unless auto-route selection is backed by fresh cached quota; `--require-secret-backend` fails unless the active SecretStore backend matches. |

The import stores token material in `~/.capd/secrets/codex/*.json` with mode
0600. SQLite stores only account metadata plus a `secret_ref`; access tokens,
refresh tokens, ID tokens, and API keys are intentionally kept out of the
database, protocol responses, and logs.

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
boot, restarts on crash, never root.

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

Transport: `ws://HOST:PORT/ws?token=TOKEN` (or `Authorization: Bearer`),
JSON-RPC 2.0. All session activity arrives as `event` notifications.

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
`{"prompt", "attachments", "accountId", "model", "effort", "capabilities", "prefer"}`.

→ `{"agent": {...}, "accountId": "codex-acct", "reason": "matched capabilities: effort, review"}`

When `accountId` is present, routing is account-aware and currently selects
Codex only, because imported account runtimes are Codex-specific. Use
`accountId:"auto"` to choose the imported Codex account with the lowest fresh
cached primary quota usage. Accounts without cached quota, or with quota rows
older than 30 minutes, are treated conservatively until `accounts/quota` or
`agents/usage` refreshes them.

### `agents/usage`

`{"agentId": "codex", "accountId": "codex-acct"}` → `{"agentId", "usage": {...}}` — agent-specific
shape; codex: `rateLimits.primary/secondary {usedPercent, windowDurationMins,
resetsAt}`, `planType`, `credits`, plus per-model buckets in
`rateLimitsByLimitId`.

### `accounts/list`

`{"provider": "codex"}` → `{"currentAccountId", "accounts": [...]}`.
Omit `provider` to list imported accounts across all providers; in that case
`currentAccountId` is omitted because current account is provider-scoped.

Returns imported account metadata and cached quota snapshots only. It never
returns `secret_ref`, access tokens, refresh tokens, ID tokens, API keys, or
raw quota JSON.

### `accounts/quota`

`{"provider": "codex", "accountId": "codex-acct"}` → `{"account": {...}}`.

Refreshes one imported Codex account through the ChatGPT backend quota endpoint,
updates the local quota snapshot, and returns the same safe account summary
shape as `accounts/list`. Omit `accountId` to refresh the current Codex account.
Use `"accountId":"auto"` to refresh the account selected by the same
conservative quota scoring rule used by account-aware routing.
The response never returns token material, `secret_ref`, or raw backend JSON.

### `session/create`

| Field | Type | Default | Meaning |
|-------|------|---------|---------|
| `agentId` | string | required | agent to drive, or `auto` to route |
| `accountId` | string | — | imported account id, or `auto` to choose the lowest fresh cached Codex quota; currently supported for Codex sessions |
| `cwd` | string | user home | project directory; must exist |
| `permissionMode` | string | `""` (default) | `acceptEdits` · `full`; `full` is rejected at filesystem root |
| `model` | string | agent default | agent-native model id |
| `effort` | string | agent default | reasoning effort (codex) |
| `resume` | string | — | agent-native session id to resume |

→ `{"sessionId": "s_..."}`; the connection is auto-subscribed to its events.

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
conversation history up to this point (agents with fork support).

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
