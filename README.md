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
`initialize`; then `agents/list`, `session/create`, `task/send`, and streamed
`event` notifications.

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

🚧 **v0.2.** End-to-end session streaming works: discovery, transport, auth,
handshake, and multi-turn sessions with native resume — verified live against
codex (two-turn memory test) and claude-code. Gemini's translator targets its
documented stream-json format, pending verification against a live login.
Next: approval flow, SQLite persistence, distribution.
The protocol spec lives in
[coding-agent-protocol](https://github.com/codingagentprotocol/coding-agent-protocol).

## License

MIT
