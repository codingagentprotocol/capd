VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LIVE_PROMPT ?= say ready
LDFLAGS := -X github.com/codingagentprotocol/capd/internal/daemon.Version=$(VERSION)

.PHONY: build run test vet tidy verify verify-secretstore live-codex-readiness

build:
	go build -ldflags "$(LDFLAGS)" -o capd ./cmd/capd

run: build
	./capd start

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

verify:
	go test ./...
	go vet ./...
	go build ./...
	go test -race ./internal/server ./internal/account/... ./cmd/capd

verify-secretstore:
	CAPD_TEST_NATIVE_SECRET=1 go test ./internal/account/secret -run TestNativeStoreRoundTrip -count=1
	GOOS=linux GOARCH=amd64 go test -c ./internal/account/secret -o /tmp/capd-secret-linux.test
	GOOS=windows GOARCH=amd64 go test -c ./internal/account/secret -o /tmp/capd-secret-windows.test.exe
	CGO_ENABLED=0 go test ./internal/account/secret

live-codex-readiness:
	go run ./cmd/capd accounts codex quota all
	go run ./cmd/capd accounts codex smoke --quota --require-multiple --require-fresh-quota --require-all-fresh-quota
	go run ./cmd/capd accounts check --json
	go run ./cmd/capd accounts check --refresh-quota --require-multiple --require-fresh-quota --require-all-fresh-quota --require-secret-backend native
	go run ./cmd/capd agents usage codex --account auto
	go run ./cmd/capd agents route --account auto --require-fresh-quota
	go run ./cmd/capd run --agent codex --account auto --require-fresh-quota "$(LIVE_PROMPT)"
