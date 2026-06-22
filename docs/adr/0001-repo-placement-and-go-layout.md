# ADR 0001 — Repo placement & Go module layout

- **Status:** Accepted
- **Date:** 2026-06-08
- **Deciders:** Romas (sole maintainer)

## Context

Trau is a standalone Go binary. This ADR records the structural decisions that
set the repo shape: where the source lives, how the binary finds the target app
repo, the module path and package layout, the binary name, and the
build/cross-compile matrix.

Because the binary is installed and run outside the target app repo, it cannot
infer the app checkout from its own on-disk location. The target repo must be
resolved explicitly or from the caller's current git checkout.

## Decision

### 1. Repo placement — standalone

The harness lives in its **own git repository**, already created at
**`github.com/RomkaLTU/trau`**. It is no longer vendored into a host app
at `scripts/trau/`.

Rationale:
- It is becoming a real, cross-compiled binary; deployment is "copy one binary"
  regardless of where the source lives, so vendoring buys nothing on that axis.
- It is developed standalone, with its own release cadence and operational docs.
- Decouples harness releases from app commits — the loop can be versioned, tagged,
  and shipped independently of any host app.

Trade-off accepted: the binary can no longer infer `REPO_ROOT` from its own location
(see §2), and the target repo must be pointed to explicitly.

### 2. Locating the target app repo (`REPO_ROOT` successor)

Resolution order, first hit wins:

1. `--repo <path>` flag
2. `TRAU_REPO_ROOT` environment variable (loadable via `trau.ini`)
3. Current working directory's git top-level (`git -C . rev-parse --show-toplevel`)

This preserves the "run it from inside the app repo" ergonomics (#3) while making
the target explicit and overridable for a standalone binary (#1, #2). All
`git`/`gh` operations continue to act on the resolved target repo, never on the
`trau` source tree. (Implemented in `internal/config`.)

### 3. Module path & layout

Module path: **`github.com/RomkaLTU/trau`** (see `go.mod`).

CLI-first binary with an embedded HTTP server (Phase 2). Standard `cmd/` + `internal/`
layout; package boundaries follow the typed seams (`Phase` / `Agent` / `Tracker`):

```
trau/
├── go.mod                       module github.com/RomkaLTU/trau
├── Makefile                     build + cross-compile (darwin/arm64, linux/amd64)
├── trau.ini(.example)          config (parsed KEY=value)
├── docs/adr/                    these decision records
├── runs/                        per-ticket artifacts (gitignored)
└── cmd/
    └── trau/main.go            CLI entry: flags, trau.ini load, dispatch
└── internal/
    ├── config/                  trau.ini load, flag parse, $PREAMBLE, repo-root resolution
    ├── agent/                   Agent iface; claude + codex impls; token-usage parsing
    ├── tracker/                 Linear-MCP seam: pick/status/quarantine/file_bug
    ├── state/                   checkpoint files, phase enum + ranking, resume/inference
    ├── pipeline/                Phase iface + build/handoff/verify/commit/merge phases
    ├── tokens/                  tokens.jsonl normalized schema + summing for --status
    ├── event/                   structured JSON event stream (the spine, from day 1)
    └── server/                  embedded HTTP status/control + SSE (Phase 2)
```

`internal/` packages are created lazily as each is implemented — this tree is the
target, not the current state. Phase-2/3 packages (`server`, formalized
interfaces) appear only when their phases begin.

### 4. Binary name — `trau`

The compiled binary is **`trau`**, preserving the CLI surface used throughout the
docs (`trau --dry-run`, `trau --status`, `trau COD-413 --once`).
The repo is named `trau`; the binary inside it is `trau`.

### 5. Build targets — static, cross-compiled

- Single static binary, **`CGO_ENABLED=0`**, `-trimpath`, `-ldflags "-s -w -X main.version=…"`.
- Cross-compile matrix: **`darwin/arm64`** (local dev) and **`linux/amd64`** (Forge server).
- Go floor: **`go 1.24`** (declared in `go.mod`).
- Driven by the `Makefile`: `make build` (host), `make dist` (release matrix into `dist/`),
  plus `fmt` / `vet` / `lint` targets. Go tests are intentionally paused until the main
  loop behavior stabilizes.

## Consequences

- **Positive:** independent versioning/release; explicit target-repo resolution; stable
  package boundaries; trivial single-binary deployment to Forge.
- **Negative / follow-ups:**
  - Target-repo resolution (§2) is user-facing surface area; errors must stay clear
    when the binary is launched outside a git checkout.
  - The standalone repo means app teams must choose how to install or pin the binary.
```
