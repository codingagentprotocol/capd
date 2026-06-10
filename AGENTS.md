# capd — agent guide

capd is a local daemon (Go) that adapts coding agent CLIs (Claude Code, Codex,
Gemini) and exposes them to web/desktop clients over CAP (JSON-RPC 2.0 on
WebSocket).

## Rules

- **Dependency direction is one-way:** `cmd → daemon → server → session →
  adapter → proc`, and everyone may import `pkg/protocol`. Never import
  upward (e.g. adapter must not know server exists).
- **`pkg/protocol` is the public contract.** Changing anything there is a
  protocol change — keep it backward compatible or bump `protocol.Version`.
- **Adapters only pass serializable protocol types** across the `Adapter` /
  `Session` interfaces. No Go-specific types may leak — adapters will move
  out-of-process later.
- **Adapters never touch `os/exec`.** Subprocess work goes through
  `internal/proc`.
- **New adapter = new package** under `internal/adapter/<name>/` with
  `adapter.go` (lifecycle) + `translate.go` (native events ↔ CAP events),
  registered in `internal/daemon/daemon.go`.
- Stdlib first. Adding a dependency needs a reason the stdlib can't cover.
- Logging via `log/slog` only. No global state; pass dependencies explicitly.

## Build & verify

```bash
go build ./...
go vet ./...
go test ./...
go run ./cmd/capd agents list   # quick end-to-end sanity check
```
