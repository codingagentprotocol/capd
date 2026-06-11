# capd

**The Coding Agent Protocol daemon.** capd runs on your machine, discovers the
coding agent CLIs you have installed — Claude Code, Codex CLI, Gemini CLI — and
exposes them to web and desktop applications through one unified protocol (CAP).

```
┌─────────────────────┐         ┌──────────────────────────────┐
│  Web / Desktop App  │  ◄CAP►  │  capd (local daemon)         │
│  (client)           │   WS    │  ├─ adapter: claude-code     │
└─────────────────────┘         │  ├─ adapter: codex           │
                                │  └─ adapter: gemini          │
                                └──────────────────────────────┘
```

## Quick start

```bash
go build -o capd ./cmd/capd

./capd agents list     # which agent CLIs does this machine have?
./capd start           # listen on ws://127.0.0.1:7777/ws
```

Clients authenticate with the token in `~/.capd/token` (generated on first
run) and speak JSON-RPC 2.0 over the WebSocket. First call must be
`initialize`. Methods:

| Group | Methods |
|-------|---------|
| agents | `agents/list`, `agents/usage` |
| session | `session/create`, `session/list`, `session/attach`, `session/fork`, `session/rollback`, `session/close` |
| task | `task/send` (text + image attachments), `task/steer`, `task/cancel`, `task/review` |
| approval | `approval/reply` |

Session events stream back as `event` notifications with per-session
sequence numbers; see `pkg/protocol/events.go` for the unified event model.

## Layout

| Path | Role |
|------|------|
| `pkg/protocol/` | CAP wire format — the only public package; SDKs build against this |
| `internal/server/` | WebSocket + token auth + method dispatch |
| `internal/session/` | Session registry; client disconnects never kill sessions |
| `internal/adapter/` | Adapter interface + one package per agent CLI |
| `internal/discovery/` | Probes which CLIs are installed |
| `internal/proc/` | Subprocess lifecycle and line-stream plumbing |
| `internal/daemon/` | Hand-written assembly of all of the above |

## Status

🚧 **v0.4 — codex app-server mode.** The codex adapter now drives
`codex app-server` (the desktop app's engine) instead of spawn-per-turn
exec, all verified live: **interactive approvals** (read-only sandbox →
approval.needed → approval/reply → command runs), **streaming deltas**,
**turn steering** (task/steer), token-usage and rate-limit push events,
explicit sandbox mapping per permission mode, with exec mode as fallback.
Plus everything from v0.3: restart-surviving sessions (SQLite), native
resume, `agents/usage`. claude-code verified in exec mode; gemini pending
a live login. Next: inspector web console, claude-code approval parity,
distribution.
The protocol spec lives in
[coding-agent-protocol](https://github.com/codingagentprotocol/coding-agent-protocol).

## License

MIT
