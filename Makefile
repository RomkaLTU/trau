# Trau loop v2 — build & cross-compile.
# Single static binary, no CGO. Targets: local macOS (darwin/arm64) and the
# Forge server (linux/amd64). See docs/adr/0001-repo-placement-and-go-layout.md.

BINARY  := trau
PKG     := ./cmd/trau
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath

export CGO_ENABLED := 0

.PHONY: all build vet test lint fmt dist clean

all: build

## build: compile for the host platform into bin/
build:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/$(BINARY) $(PKG)

## vet: static checks
vet:
	go vet ./...

## test: compile/race-check packages; Go tests are intentionally absent for now
test:
	go test -race ./...

## lint: golangci-lint (install separately)
lint:
	golangci-lint run

## fmt: format all sources
fmt:
	gofmt -w .

## dist: cross-compile the release matrix into dist/
dist: dist/$(BINARY)-darwin-arm64 dist/$(BINARY)-linux-amd64

dist/$(BINARY)-darwin-arm64:
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $@ $(PKG)

dist/$(BINARY)-linux-amd64:
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $@ $(PKG)

## clean: remove build artifacts
clean:
	rm -rf bin dist
