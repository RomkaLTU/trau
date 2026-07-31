# Trau loop v2 — build & cross-compile.
# Single static binary, no CGO. Targets: local macOS (darwin/arm64) and the
# Forge server (linux/amd64). See docs/adr/0001-repo-placement-and-go-layout.md.

BINARY  := trau
PKG     := ./cmd/trau
# No redirects or ||: parse-time $(shell) runs under cmd.exe when make is
# launched from PowerShell (no sh on PATH), and cmd reads 2>/dev/null as a
# redirect to the literal path \dev\null. $(or) keeps the dev fallback for
# builds outside a git checkout.
VERSION ?= $(or $(shell git describe --tags --always --dirty),dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath

# The host OS decides two things: whether the binary needs a .exe suffix for
# anything but Git Bash to execute it, and whether the race detector needs cgo —
# it does on windows, where -race has no pure-Go implementation.
HOST_GOOS := $(shell go env GOOS)
ifeq ($(HOST_GOOS),windows)
EXE          := .exe
CGO_FOR_TEST := 1
else
EXE          :=
CGO_FOR_TEST := 0
endif

NPM      ?= npm
WEB_DIR  := web
WEB_DIST := internal/webserver/dist/index.html
# Pure make instead of find, for the same reason VERSION shuns the shell: the
# list must come out identical no matter what launched make. Directories are
# listed alongside files, matching the find this replaces, so adding or
# removing a web source still re-triggers the SPA build.
rwildcard = $(foreach p,$(wildcard $(1)/*),$(call rwildcard,$(p)) $(p))
WEB_SRC  := $(wildcard $(WEB_DIR)/src $(WEB_DIR)/public $(WEB_DIR)/index.html $(WEB_DIR)/package.json $(WEB_DIR)/vite.config.ts) $(call rwildcard,$(WEB_DIR)/src) $(call rwildcard,$(WEB_DIR)/public)

# Only names the port in the hub-guard message; the binary resolves the real one
# from the layered config.
SERVE_PORT ?= 8728

# The shipped binary stays cgo-free on every platform (ADR 0023). Only `make
# test` overrides this, and only where the race runtime leaves it no choice.
export CGO_ENABLED := 0

.PHONY: all build reset hub-guard race-guard net-guard web vet windows test lint fmt dist clean

all: build

## build: compile the SPA + binary for the host platform into bin/
build: web
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/$(BINARY)$(EXE) $(PKG)

## reset: rebuild the dev binary + make the local web hub run it
reset: hub-guard build
	@./bin/$(BINARY)$(EXE) hub restart

## hub-guard: refuse to restart the hub from inside a trau-managed agent run
hub-guard:
	@if [ "$$TRAU_ACTIVE" = "1" ]; then \
		echo "✗ refusing 'make reset' inside a trau-managed run: the hub on :$(SERVE_PORT) owns this run,"; \
		echo "  and restarting or killing it loses the run's data. For live QA start an isolated hub instead:"; \
		echo "    iso=\$$(mktemp -d) && TRAU_HOME=\$$iso/.trau HOME=\$$iso ./bin/$(BINARY)$(EXE) serve --port 8799"; \
		echo "  and kill only that pid when done. Never touch the hub on :$(SERVE_PORT)."; \
		exit 1; \
	fi

## web: build the embedded SPA (only when its sources change)
web: $(WEB_DIST)

$(WEB_DIST): $(WEB_SRC)
	cd $(WEB_DIR) && $(NPM) ci && $(NPM) run build

## vet: static checks, including the native-Windows compile gate
vet: windows
	go vet ./...

## windows: prove the tree still compiles for native Windows (ADR 0023)
windows:
	GOOS=windows go build ./...

## race-guard: refuse `make test` when the race detector's cgo needs are unmet
race-guard:
	@if [ "$(CGO_FOR_TEST)" = "1" ] && ! command -v $${CC:-gcc} >/dev/null 2>&1; then \
		echo "✗ 'make test' needs a C compiler on $(HOST_GOOS): -race requires cgo here."; \
		echo "    scoop install mingw"; \
		echo "  Already installed? It adds its own bin/ to PATH; open a new shell."; \
		exit 1; \
	fi

## net-guard: refuse `make test` when a test package skips the network guard
net-guard:
	go run ./internal/netguard/check

## test: run the suite under the race detector
test: race-guard net-guard
	CGO_ENABLED=$(CGO_FOR_TEST) go test -race ./...

## lint: golangci-lint (install separately)
lint:
	golangci-lint run

## fmt: format all sources
fmt:
	gofmt -w .

## dist: cross-compile the release matrix into dist/
dist: web dist/$(BINARY)-darwin-arm64 dist/$(BINARY)-linux-amd64

dist/$(BINARY)-darwin-arm64:
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $@ $(PKG)

dist/$(BINARY)-linux-amd64:
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $@ $(PKG)

## clean: remove build artifacts
clean:
	rm -rf bin dist
