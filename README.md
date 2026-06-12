# capd

**One protocol to drive every coding agent CLI.**

capd is the Coding Agent Protocol (CAP) daemon. It runs on your machine,
discovers the coding agent CLIs you have installed — Codex, Claude Code,
Gemini CLI, OpenCode, Cursor CLI, and more — and exposes them to web,
desktop, and terminal clients through a single WebSocket + JSON-RPC 2.0
interface.

Every agent CLI speaks its own dialect: different flags, session models, and
streaming formats. capd translates all of them into one unified protocol, so
a client written once can drive any agent.

## Architecture at a glance

```mermaid
flowchart LR
    web["Web app"]
    desktop["Desktop app"]
    cli["capd run / watch"]

    subgraph cap["capd daemon"]
        server["server\nWebSocket + JSON-RPC\nmethod dispatch + fan-out"]
        session["session\nlive registry\nseq event log\nSQLite persistence"]
        adapter["adapter\none translator per agent dialect"]
        proc["proc\nsubprocess lifecycle\nstdout/stderr stream plumbing"]
        account["account\nsafe account metadata\nquota snapshots\nsession binding"]

        server --> session
        session --> adapter
        adapter --> proc
        server -. account-aware routing .-> account
        session -. session-account map .-> account
    end

    codex["Codex CLI\napp-server profile pool"]
    claude["Claude Code\nheadless exec"]
    gemini["Gemini / Qwen / iFlow\nheadless exec"]
    opencode["OpenCode\nheadless exec"]
    cursor["Cursor CLI\nheadless exec"]

    web -- "CAP over WebSocket\nJSON-RPC 2.0" --> server
    desktop -- "CAP over WebSocket\nJSON-RPC 2.0" --> server
    cli -- "local client" --> server

    proc --> codex
    proc --> claude
    proc --> gemini
    proc --> opencode
    proc --> cursor
```

The dependency direction stays one-way:

```mermaid
flowchart LR
    cmd["cmd/capd"] --> daemon["internal/daemon"]
    daemon --> server["internal/server"]
    server --> session["internal/session"]
    session --> adapter["internal/adapter"]
    adapter --> proc["internal/proc"]
    protocol["pkg/protocol\npublic CAP contract"]

    cmd -.-> protocol
    server -.-> protocol
    session -.-> protocol
    adapter -.-> protocol
```

## Why capd

- **Write once, drive any agent.** One client codebase for Codex, Claude
  Code, Gemini, OpenCode, Cursor — and forks like Qwen Code, iFlow, and
  CodeBuddy ride the same translators.
- **Sessions that refuse to die.** Client disconnects, daemon restarts, and
  even agent-engine crashes never lose a conversation: events are sequence-
  numbered and persisted, and sessions revive with their native context.
- **Real control, not fire-and-forget.** Stream output token by token, steer
  a running turn, cancel instantly, and approve or deny each dangerous action
  from any client.
- **Tiny footprint.** A single CGO-free binary; ~18 MB resident as a daemon.

## Install

Download a release archive for your platform from
[Releases](https://github.com/codingagentprotocol/capd/releases), or build
from source:

```bash
go install github.com/codingagentprotocol/capd/cmd/capd@latest
```

Run it in the foreground, or install it as a user-level service
(launchd / systemd / Windows SCM — starts on boot, restarts on crash,
never runs as root):

```bash
capd start                                  # foreground
capd service install && capd service start  # persistent
```

The local web console is served by the daemon at
`http://127.0.0.1:7777/console/`. Pass the daemon token from `~/.capd/token`
as `?token=...` when opening it in a browser, or print a ready-to-open local
URL with `capd token --url`.

## Quick start

```bash
capd agents list      # which agent CLIs does this machine have?

cd ~/your-project
capd run "explain the structure of this repo"
```

`capd run` streams the agent's work to your terminal: typewriter output,
each command it executes, and interactive approval prompts:

```
session s_824067bfb24f2c25 (codex)
⏵ /bin/zsh -lc 'ls -la'
⚠ approval needed (command): rm -rf build/
  allow? [y]es / [a]lways / [N]o: y
✓ done
(continue with: capd run --session s_824067bfb24f2c25 "...")
```

Useful flags and companions:

```bash
capd run --agent opencode "..."             # pick the agent
capd run --agent codex --account codex-acct # use an imported Codex account
capd run --agent codex --account auto --require-fresh-quota "..." # quota-aware Codex account
capd run --model gpt-5.3-codex --effort high "..."
capd run --permission acceptEdits "..."     # default | acceptEdits | full
capd run --image diagram.png "what's wrong in this architecture?"
capd run --session s_xxx "follow-up..."     # multi-turn, survives restarts

capd sessions             # all sessions: live / stored / ended
capd watch s_xxx          # re-join a long-running task: replay + follow
capd agents usage codex   # plan, rate-limit windows, reset times
capd agents usage codex --account codex-acct
capd agents usage codex --account auto
capd agents route --account auto --require-fresh-quota --json

capd accounts codex import   # import local ~/.codex/auth.json into capd
capd accounts codex list     # imported Codex accounts, current account marked
capd accounts codex project  # create a per-account CODEX_HOME projection
capd accounts codex quota    # refresh quota and print a safe summary
capd accounts codex quota auto
capd accounts codex quota all
capd accounts codex smoke --quota --require-multiple --require-fresh-quota --require-all-fresh-quota
capd start                   # keep running in another terminal for CAP/WebSocket checks
capd accounts check --json   # daemon-side accounts/check smoke evidence
capd accounts check --refresh-quota --require-multiple --require-fresh-quota --require-all-fresh-quota --require-secret-backend native
capd agents route --account auto --require-fresh-quota
```

Every flag, protocol field, and event is documented in
[docs/reference.md](docs/reference.md).

### Permission modes

| Mode | Meaning (codex mapping) |
|------|-------------------------|
| `default` | read-only sandbox; every write needs an approval |
| `acceptEdits` | workspace-write; actions outside the workspace need approval |
| `full` | no sandbox, no prompts — you opted in |

capd sets these explicitly per session and never silently inherits a
permissive user config.

## Supported agents

| Agent | Mode | Streaming | Approvals | Steer | Fork/Rollback | Usage data |
|-------|------|:---:|:---:|:---:|:---:|:---:|
| Codex CLI | app-server engine | ✅ deltas | ✅ | ✅ | ✅ | ✅ |
| Claude Code | headless exec | block | — | — | — | — |
| OpenCode | headless exec | block | — | — | — | — |
| Gemini CLI | headless exec | pending login verification | | | | |
| Cursor CLI | headless exec | pending login verification | | | | |
| Qwen Code, iFlow | gemini-family translators; discovered when installed | | | | | |
| CodeBuddy | claude-family translator; discovered when installed | | | | | |
| Kimi CLI | discovery only; calibration pending | | | | | |

Adding a fork-family CLI is one registry line; a brand-new dialect is two
pure functions (build the command, translate its stream).

## The protocol

JSON-RPC 2.0 over `ws://127.0.0.1:7777/ws?token=<~/.capd/token>`. First call
must be `initialize` (version negotiation).

| Group | Methods |
|-------|---------|
| agents | `agents/list`, `agents/route`, `agents/usage` |
| accounts | `accounts/list`, `accounts/quota` |
| session | `session/create`, `session/list`, `session/attach`, `session/history`, `session/fork`, `session/rollback`, `session/close` |
| task | `task/send` (text + image attachments), `task/steer`, `task/cancel`, `task/review` |
| approval | `approval/reply` (`approve` / `approveAlways` / `deny`) |

Session activity streams back as `event` notifications, each stamped with a
per-session monotonic `seq` — reconnect with `session/attach {fromSeq}` and
miss nothing. The unified event model (10 types: `output.text` with deltas,
`tool.use`/`tool.result`, `approval.needed`, `usage.updated`, `task.done`,
…) lives in [`pkg/protocol`](pkg/protocol), the only public Go package and
the protocol's source of truth.

A dependency-free browser client demonstrating the full surface — agent
picker, project directory, streaming, approval buttons — is in
[`examples/web`](examples/web).

## Resilience model

Any single link can die without losing a conversation:

```mermaid
sequenceDiagram
    participant Client
    participant Server as capd server
    participant Session as session manager
    participant DB as SQLite event log
    participant Agent as agent CLI

    Client->>Server: session/create
    Server->>Session: create live session
    Session->>Agent: start native session
    Agent-->>Session: streamed events
    Session-->>DB: persist seq events
    Session-->>Client: event notifications

    Client--xServer: disconnect
    Agent-->>Session: keeps running
    Session-->>DB: keeps appending events

    Client->>Server: session/attach fromSeq
    Server->>Session: resolve or revive
    Session-->>DB: load missed events
    Session-->>Client: replay + live follow

    Agent--xSession: engine exits
    Session-->>Client: error + task.done engineDied
    Client->>Server: next touch
    Session->>Agent: fresh process + native resume
```

- **Client drops** → events keep accumulating; re-attach replays from your
  last `seq`.
- **Daemon restarts** → session identity and the event log live in SQLite
  (`~/.capd/capd.db`); the next touch revives the session and resumes the
  agent's native conversation.
- **Agent engine crashes** → detected instantly on pipe EOF; sessions get an
  error event and revive on a fresh engine, history intact.
- WebSocket heartbeat (30 s ping) reaps dead client connections; `GET
  /healthz` for monitors.

## Security

```mermaid
flowchart TB
    client["Client request"]
    auth["daemon auth\nCAPD token\nconstant-time compare"]
    origin["browser origin allowlist\nlocalhost by default"]
    policy["request policy\ncwd, permission mode,\nattachments, approvals"]
    headers["header guard\nno client Authorization/Cookie/X-API-Key\nCRLF rejection"]
    redact["redaction\nmask secrets\nshorten account ids"]
    account["account control plane\nSQLite metadata only\nno tokens"]
    secret["secret plane\nSecretStore\nnative OS backend / file fallback"]
    runtime["runtime projection\nper-account CODEX_HOME\nCodex app-server profile"]
    upstream["agent / OpenAI upstream"]

    client --> auth --> origin --> policy --> headers
    headers --> redact
    headers --> runtime --> upstream
    account -. secret ref only .-> secret
    account --> runtime
```

- Binds `127.0.0.1` by default; remote exposure is an explicit choice
  (`--host`, TLS via your reverse proxy).
- Token auth on every connection (`~/.capd/token`, 0600, generated on first
  run).
- Browser `Origin` allowlist: localhost always, anything else via
  `--origins` / `CAPD_ORIGINS` — never default-open.
- Sessions declare sandbox and approval policy explicitly; unknown approval
  requests are denied by default.
- Client-supplied sensitive headers (`Authorization`, `Cookie`, `X-API-Key`,
  `Proxy-Authorization`, …) are rejected at trust boundaries.
- Header values are checked for newline injection; diagnostics use redacted
  headers so access tokens never land in logs or protocol events.
- See [docs/testing.md](docs/testing.md) for standard regression, native
  SecretStore, and live Codex account smoke commands.
- `capd accounts check --json` exercises the running daemon's CAP
  `accounts/check` RPC and therefore requires `capd start`; `capd accounts
  codex smoke` performs a direct local CLI smoke check without the daemon.
- The Web Console exposes the same daemon-side evidence with an `accounts/check`
  readiness gate for multi-account, fresh-quota, and optional native SecretStore
  checks; that gate can refresh quota for every imported Codex account before it
  validates freshness.
- Codex account support is split into a control plane and a runtime plane:
  SQLite stores account metadata and quota snapshots, while each runtime can
  use its own `CODEX_HOME` and app-server profile.
- `capd accounts codex import` copies token material into capd's secret
  store, records only metadata in SQLite, and never writes back to the user's
  original `~/.codex`.
- Codex owns the ChatGPT OAuth refresh flow. When Codex refreshes a
  capd-managed per-account `auth.json`, capd syncs the newer projected token
  bundle back into SecretStore before the next projection, with per-account
  runtime locking to avoid concurrent refresh-file races.
- Secret storage defaults to the local file backend (`0600`). Set
  `CAPD_SECRET_BACKEND=native` to use the OS secret backend where implemented;
  macOS stores bundles in the user Keychain, Windows uses Credential Manager,
  Linux uses Secret Service via `secret-tool`, and unsupported platforms fail
  closed instead of silently falling back.
- New Codex sessions can opt into an imported account with `--account` or
  protocol `session/create.accountId`; `accountId:"auto"` uses conservative
  quota scoring. Fresh cached primary quota uses the actual usage percent, while
  rows older than 30 minutes or missing quota are treated like unknown usage so
  stale low-usage snapshots do not dominate routing. The daemon projects that
  account into a dedicated `CODEX_HOME` and the Codex app-server profile pool
  keeps it isolated from other accounts.

## Repository layout

| Path | Role |
|------|------|
| `pkg/protocol/` | CAP wire format — public contract; SDKs build against this |
| `internal/server/` | WebSocket, auth, dispatch, per-connection fan-out |
| `internal/session/` | Session registry, seq event log, SQLite store |
| `internal/account/` | Account metadata, quota snapshots, safe Codex header builders |
| `internal/adapter/` | Adapter engine + one package per agent dialect |
| `internal/discovery/` | Probes which CLIs are installed |
| `internal/proc/` | Subprocess lifecycle and line-stream plumbing |
| `internal/daemon/` | Hand-written assembly of all of the above |
| `cmd/capd/` | CLI: start, run, agents, sessions, service |
| `examples/web/` | Browser client demo |

Dependency direction is strictly one-way: `cmd → daemon → server → session
→ adapter → proc`; everyone may import `pkg/protocol`, never upward.

## Development

```bash
go build ./... && go vet ./... && go test ./...
capd run --json "..."      # raw event stream for debugging
```

The test suite covers translators (calibrated against captured real CLI
streams), the session store, and a protocol-level integration suite that
drives a real WebSocket server against a scripted adapter.

## Status and roadmap

v0.1.0, verified end to end against live agents. Next: the inspector web
console, Claude Code deep alignment (interactive approvals via its
stream-json control protocol), and an out-of-process adapter SDK so the
community can add agents in any language.

The protocol specification lives in
[coding-agent-protocol](https://github.com/codingagentprotocol/coding-agent-protocol).

## License

MIT
