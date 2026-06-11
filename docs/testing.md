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

## Native SecretStore

The default test suite compiles every native backend but only touches real OS
secret storage when explicitly requested.

```bash
CAPD_TEST_NATIVE_SECRET=1 go test ./internal/account/secret -run TestNativeStoreRoundTrip -count=1
GOOS=linux GOARCH=amd64 go test -c ./internal/account/secret -o /tmp/capd-secret-linux.test
GOOS=windows GOARCH=amd64 go test -c ./internal/account/secret -o /tmp/capd-secret-windows.test.exe
CGO_ENABLED=0 go test ./internal/account/secret
```

Linux native storage requires `secret-tool` from libsecret and an unlocked
Secret Service session.

## Codex Account Smoke

Import at least one Codex `auth.json`, then run the local smoke check:

```bash
capd accounts codex import
capd accounts codex smoke
```

For real quota validation:

```bash
capd accounts codex smoke --quota
```

For multi-account routing readiness:

```bash
capd accounts codex smoke --quota --require-multiple
capd agents usage codex --account auto
```

The smoke command verifies imported account metadata, SecretStore readability,
per-account `CODEX_HOME` projection, private `auth.json` permissions, and
optionally ChatGPT backend quota refresh. It prints only account metadata,
projection paths, and quota percentages; token material is never printed.
