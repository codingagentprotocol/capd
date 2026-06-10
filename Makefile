VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/codingagentprotocol/capd/internal/daemon.Version=$(VERSION)

.PHONY: build run test vet tidy

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
