# Codex Account Plane Evidence Matrix

This matrix is the completion audit for the Codex multi-account plan. It maps
each product requirement to the strongest current evidence, the deterministic
gate that should keep it working, and the live/native evidence still needed
before declaring the full goal complete.

## Requirement Matrix

| Requirement | Current proof surface | Deterministic gate | Live/native gate | Completion state |
|-------------|-----------------------|--------------------|------------------|------------------|
| Codex account import and metadata | `capd accounts import`, `capd accounts list --json`, SQLite account metadata, safe current-account markers | `make verify-codex-readiness-sim` covers CLI and daemon import paths plus metadata redaction | Import two real Codex `auth.json` files with `capd accounts import --auth ... --auth ...` using the selected SecretStore backend | Implemented; live proof depends on local credentials |
| Quota query and cache | `capd accounts codex quota all --timeout 2m`, `accounts/quota`, cached quota snapshots with fresh/stale/missing state | `make verify-codex-readiness-sim` covers quota parsing, invalid percent hardening, partial failure evidence, and concurrent quota refresh | `CAPD_SECRET_BACKEND=native capd accounts --secret-backend native codex quota all --timeout 2m` | Implemented; native live quota must be refreshed per machine |
| Account-aware routing | `capd agents route --account auto --require-fresh-quota --json`, `session/create.accountId:"auto"`, route candidates, route policy, selected account reason | `make verify-codex-readiness-sim` covers deterministic score ordering, stale/missing quota conservatism, profile filters, fresh-quota failure evidence, and route-candidate redaction | `capd agents route --account auto --profile <name> --require-fresh-quota --json` after fresh live quota | Implemented with conservative quota-pressure policy; health/failure/task-class scoring is next evolution |
| SecretStore native backend | `capd secretstore check --json --roundtrip --require-backend native --timeout 2m`, native macOS/Linux/Windows backends, migration readback safety | `make verify-secretstore` compiles native backends, exercises opt-in native roundtrip, Linux stdin safety, Windows chunking, and CGO-free fallback | `CAPD_TEST_NATIVE_SECRET=1 go test ./internal/account/secret -run TestNativeStoreRoundTrip -count=1` and native `accounts check --readiness` | Implemented; OS approval and backend availability remain environment-specific |
| Prompt-free Web diagnostics | `capd console`, `capd console --probe`, `/probe/data`, console diagnostic refresh, probe `Refresh` path | `make verify-codex-readiness-sim` covers prompt-free probe/console contracts, probe summaries, readiness backend defaults, browser token cleanup, and security headers | Open `capd console --probe --require-secret-backend native` and run `capd probe data --json --readiness --require-secret-backend native --fail` | Implemented; browser rendering should be spot-checked after UI changes |
| Full Web Console task control | `console` scoped token, `session/create`, `task/send`, `task/cancel`, approval reply paths | `make verify-codex-readiness-sim` includes scoped token tests proving `console` can task-control while `console:read` and `probe:read` reject task control | Open `capd console` with a running daemon and create a Codex session through the page | Implemented after scope split; live UI run is still the strongest proof |
| Evidence package and support bundle | `capd probe evidence --manifest ... --fail`, `capd support bundle --out ...`, `manifest.json`, `audit.json`, `report.html` | `make verify-codex-readiness-sim` covers evidence manifest parsing, route proof, fresh quota proof, audit unsafe-field rejection, and support bundle audit indexing | `CAPD_LIVE_EVIDENCE_DIR=/tmp/capd-live-evidence make live-codex-selftest` then `capd probe evidence --manifest /tmp/capd-live-evidence/manifest.json --fail` | Implemented; archived live package proves release readiness |
| Runtime stability and reconnect | `session/attach`, `session/history`, persisted sessions, bounded subscriber buffers, overflow marker | `make verify` covers disconnect/reconnect continuity, backpressure, replay, and daemon restart recovery tests | Leave a live session running, disconnect browser/CLI, reconnect with `capd run --session <id> ...` or Web attach | Implemented in deterministic tests; live long-run soak is still valuable |
| Security by contract | scoped tokens, security headers, redacted account/probe/support/audit JSON, no SecretStore refs or token material in public evidence | `make verify` and `make verify-codex-readiness-sim` cover scope mismatch, headers, redaction, unsafe audit evidence, and prompt-free diagnostics | Review generated support bundle and probe output from the live machine before sharing | Implemented; keep expanding endpoint-specific leak tests |

## Release Audit Commands

Run these before treating a change as release-sized:

```bash
make verify
make verify-codex-readiness-sim
make verify-secretstore
```

Run these when real native Codex accounts are available:

```bash
CAPD_SECRET_BACKEND=native capd accounts import --auth /tmp/acct-a/auth.json --auth /tmp/acct-b/auth.json
CAPD_SECRET_BACKEND=native capd accounts --secret-backend native codex quota all --timeout 2m
CAPD_SECRET_BACKEND=native capd accounts --secret-backend native codex smoke --json --quota --require-multiple --require-fresh-quota --require-all-fresh-quota --require-secret-backend native --timeout 2m
CAPD_SECRET_BACKEND=native capd secretstore check --json --roundtrip --require-backend native --timeout 2m
CAPD_SECRET_BACKEND=native capd accounts check --json --readiness --require-secret-backend native --timeout 2m
capd agents route --account auto --require-fresh-quota --json
capd console --probe --require-secret-backend native
capd probe data --json --readiness --require-secret-backend native --timeout 2m --fail
CAPD_LIVE_EVIDENCE_DIR=/tmp/capd-live-evidence make live-codex-selftest
capd probe evidence --manifest /tmp/capd-live-evidence/manifest.json --fail
capd support bundle --out /tmp/capd-support --require-secret-backend native
```

## Not Complete Until

Do not mark the full Codex multi-account goal complete unless current evidence
proves all of the following on this machine:

- at least two Codex accounts are imported through the daemon-side path;
- the selected backend is native and passes a SecretStore roundtrip;
- every imported Codex account has fresh quota evidence;
- `--account auto --require-fresh-quota` selects a fresh account with safe
  route policy and route-candidate evidence;
- the Web probe readiness endpoint passes with `requireSecretBackend=native`;
- the full Web Console can create a session using a `console` scoped token;
- `capd probe evidence --manifest ... --fail` passes against the saved live
  evidence package;
- generated evidence and support bundles contain no access tokens, refresh
  tokens, raw auth JSON, SecretStore refs, credential strings, or local runtime
  paths intended only for the host.

## Next Evolution Demands

- Add configurable scheduler weights for account health, recent failures, task
  class, and user intent while preserving the conservative default.
- Persist a safe token-health timeline of quota and route decisions so the
  console can show drift and repeated failure patterns without storing tokens.
- Let the Web Console run approved repair-plan steps directly with audit events.
- Add a browser-level smoke test that opens `/console/`, creates a fake session
  with a `console` scoped token, and verifies prompt-free diagnostics visually.
- Expand native SecretStore recovery hints for macOS Keychain, Windows
  Credential Manager, and Linux Secret Service.
