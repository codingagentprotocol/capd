# capd Evolution Roadmap

This roadmap turns the current Codex multi-account work into concrete product
requirements. The goal is a local agent control plane that stays portable,
auditable, and safe while supporting more agent CLIs and richer clients.

## Product North Star

capd should become the local, protocol-stable cockpit for coding agents:

- one CAP client can drive every supported coding agent CLI;
- sessions survive client disconnects and daemon restarts;
- account, quota, routing, approval, and evidence data are available without
  leaking tokens;
- web and desktop clients can diagnose problems without shell access;
- live validation produces portable evidence packages that can be archived,
  reviewed, and replayed.

## P0 Stabilization

### Live Evidence Gate

**Requirement:** every release-sized change must produce deterministic evidence
and, when live credentials are available, a portable live evidence package.

**Acceptance:**

- `make verify` passes.
- `make verify-codex-readiness-sim` passes.
- `capd probe evidence --manifest <manifest.json> --fail` validates route
  policy, route decision, route candidates, fresh quota, backend, daemon mode,
  and selftest status.
- Evidence packages never embed access tokens, refresh tokens, local shell logs,
  or raw SecretStore payloads.

### Prompt-Free Diagnostics

**Requirement:** ordinary console and probe refreshes must not trigger native
SecretStore prompts.

**Acceptance:**

- `/probe/data` and the console default diagnostic refresh use safe metadata and
  route evidence only.
- Secret-reading checks require explicit readiness intent through
  `--readiness`, `--require-secret-backend`, or the readiness UI action.
- Keychain access-denied errors produce actionable recovery commands.

### Route Evidence Compatibility

**Requirement:** evidence readers must accept real `agents route` output, not
only synthetic probe summaries.

**Acceptance:**

- Top-level `accountRoute`, `routeCandidates`, and `routePolicy` are enough to
  prove route decision evidence.
- Nested `data.accountRoute`, `data.routeDecision`, `codex.accountRoute`, and
  `summary.routeDecisionOk` remain supported for backward compatibility.

## P1 Account Intelligence

### Account-Aware Scheduler

**Requirement:** `--account auto` should evolve from a simple selector into a
policy-driven scheduler.

**Design:**

- Keep policy data in serializable protocol structs.
- Score candidates with quota pressure, freshness, account health, current
  account tie-break, and optional workload fit.
- Keep routing explainable: every decision must include policy, candidate list,
  selected account, and reason.

**Acceptance:**

- Route decisions are deterministic for equal inputs.
- Stale or missing quota is conservatively scored and clearly labeled.
- `--require-fresh-quota` fails with safe partial evidence and runnable next
  steps.

### Multi-Account Profiles

**Requirement:** support named local account groups for personal, work, and CI
contexts.

**Design:**

- Store profile metadata in the existing account metadata store.
- Keep token material in SecretStore only.
- Allow commands such as `capd accounts profile list`,
  `capd accounts profile use <name>`, and
  `capd run --agent codex --profile work --account auto`.

**Acceptance:**

- Profiles work on macOS, Linux, and Windows.
- Switching profiles never copies token material into logs or config files.
- Route evidence includes the selected profile name when present.

## P2 Security Hardening

### Capability-Scoped Tokens

**Requirement:** daemon tokens should be scoped by use case.

**Design:**

- Keep the current local token for compatibility.
- Add optional token scopes such as `console:read`, `sessions:write`,
  `accounts:read`, and `accounts:readiness`.
- Let console URLs carry the narrowest token needed for the page.

**Acceptance:**

- Unauthorized methods return JSON-RPC errors without revealing resource state.
- Security headers remain enforced for console and probe pages.
- Tests cover method/scope mismatches.

### Audit Log

**Requirement:** record security-relevant local actions without secrets.

**Events:**

- account import and migration;
- SecretStore backend checks;
- route decisions;
- approval decisions;
- repair runner execution.

**Acceptance:**

- Audit events are append-only, bounded, and redacted.
- Users can export a support bundle with audit metadata plus evidence reports.

## P3 Performance And Reliability

### Session Backpressure

**Requirement:** slow web clients must not stall agent process reads.

**Design:**

- Keep subprocess stdout/stderr draining independent of WebSocket delivery.
- Persist event batches before fan-out.
- Add bounded per-client buffers with explicit dropped/replay markers.

**Acceptance:**

- A slow client can reconnect and replay from the last sequence number.
- Large output turns do not increase daemon memory without bound.

### Health And Metrics

**Requirement:** expose local operational metrics without adding a heavy
dependency.

**Metrics:**

- active sessions;
- connected clients;
- event backlog;
- adapter process starts/failures;
- route decisions by agent and backend;
- SecretStore access-denied counts.

**Acceptance:**

- `/healthz?format=json` remains lightweight and secret-free.
- Optional metrics are disabled or local-only by default.

## P4 Extensibility

### Adapter Conformance Suite

**Requirement:** every adapter should pass the same lifecycle contract before
it is considered supported.

**Acceptance:**

- Start, stream, continue, cancel, approval, image input, and resume behavior
  are tested through protocol-level fixtures.
- New adapters live under `internal/adapter/<name>/` and do not import upward.
- Subprocess execution stays inside `internal/proc`.

### Out-Of-Process Adapter Boundary

**Requirement:** prepare adapters to move out of process later.

**Acceptance:**

- Adapter interfaces pass only serializable protocol types.
- No Go-only concrete types leak through adapter/session contracts.
- Failure evidence remains stable when an adapter crashes.

## Suggested Next Build Order

1. Done: add route-evidence compatibility tests around real `agents route` JSON.
2. Done: add a `capd support bundle` command that packages safe evidence,
   health, route, prompt-free doctor, optional probe data, and HTML reports.
3. Add scoped daemon tokens for console/probe URLs.
4. Add profile-aware routing metadata and CLI commands.
5. Add session backpressure stress tests before changing event fan-out internals.
6. Add adapter conformance fixtures for Codex first, then clone them for other
   adapters.
