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

```
┌─────────────┐  ┌─────────────┐  ┌─────────────┐
│   Web app   │  │ Desktop app │  │  capd run   │   clients
└──────┬──────┘  └──────┬──────┘  └──────┬──────┘
       └────────────────┼────────────────┘
                ws://127.0.0.1:7777/ws       CAP: JSON-RPC 2.0,
                        │                    token auth, origin allowlist
┌───────────────────────┴───────────────────────┐
│                     capd                      │
│  server    method dispatch · event fan-out    │
│  session   seq event log · SQLite persistence │
│  adapter   one translator per agent dialect   │
│  proc      subprocess & stream plumbing       │
└──┬──────────┬──────────┬──────────┬───────────┘
   │          │          │          │
 codex      claude     gemini    opencode  ···    agent CLIs
 (app-server engine)   (headless exec mode)
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
capd run --model gpt-5.3-codex --effort high "..."
capd run --permission acceptEdits "..."     # default | acceptEdits | full
capd run --image diagram.png "what's wrong in this architecture?"
capd run --session s_xxx "follow-up..."     # multi-turn, survives restarts

capd sessions             # all sessions: live / stored / ended
capd watch s_xxx          # re-join a long-running task: replay + follow
capd agents usage codex   # plan, rate-limit windows, reset times
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
| agents | `agents/list`, `agents/usage` |
| session | `session/create`, `session/list`, `session/attach`, `session/fork`, `session/rollback`, `session/close` |
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

- Binds `127.0.0.1` by default; remote exposure is an explicit choice
  (`--host`, TLS via your reverse proxy).
- Token auth on every connection (`~/.capd/token`, 0600, generated on first
  run).
- Browser `Origin` allowlist: localhost always, anything else via
  `--origins` / `CAPD_ORIGINS` — never default-open.
- Sessions declare sandbox and approval policy explicitly; unknown approval
  requests are denied by default.

## Repository layout

| Path | Role |
|------|------|
| `pkg/protocol/` | CAP wire format — public contract; SDKs build against this |
| `internal/server/` | WebSocket, auth, dispatch, per-connection fan-out |
| `internal/session/` | Session registry, seq event log, SQLite store |
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
