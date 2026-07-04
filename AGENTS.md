# AGENTS.md

Guidance for AI coding agents working in this repository.

## What this is

Trau is an autonomous, ticket-driven development loop shipped as a single static Go binary (`CGO_ENABLED=0`). It picks a ready tracker ticket (Linear/Jira/GitHub Issues), drives it through **build → handoff → verify → commit → PR → CI → merge** with one fresh AI-agent subprocess per phase, checkpoints after each phase, and resumes after any interruption.

Read `CONTEXT.md` first — it defines the domain vocabulary (**Provider / Model / Route / Fallback provider / Provider override**) and which near-synonyms to avoid. Use those terms exactly in code, comments, and tickets.

## Commands

```bash
make build                 # compile to bin/trau
make test                  # go test -race ./...
go test -race ./internal/pipeline -run TestVerifyPause   # single test
make vet                   # go vet ./...
make lint                  # golangci-lint (installed separately)
make fmt                   # gofmt -w .
make dist                  # cross-compile release matrix into dist/
```

Commit messages follow Conventional Commits (`feat(scope):`, `fix:`, …) — release notes are generated from them (ADR 0002). Ticket IDs in commits/comments reference the project's own tracker tickets and act as the design changelog.

## Architecture

`cmd/trau/main.go` is the composition root (~2100 lines): flag parsing, layered config, and the builders (`buildRouter`, `buildTracker`, `buildPipeline`, `buildFallback`) that wire everything through injected interfaces. All modes (TUI session, headless, `--once`, `--dry-run`) converge on `runLoop(ctx, engine, ...)`, tested against the small `engine` interface. Subcommands: `doctor` (preflight) and `watch` (read-only transcript tail via `internal/vterm`).

The target repo is never the trau source tree — it resolves via `--repo` flag → `TRAU_REPO_ROOT` → cwd's git toplevel (ADR 0001). All `git`/`gh` operations act on the resolved target.

### Package seams (internal/)

- **pipeline** — the phase chain. Verify is a *cold, adversarial* pass: the verifier agent sees only the handoff brief (`handoff.md` + `rubric.json`) and the code on disk, never the build agent's reasoning. On verify failure: up to `MaxRepairs` repairs, then `MaxBugfixes` bugfixes, then quarantine. Error taxonomy is the core invariant (`classifyPhaseErr`): `GiveUpError` → quarantine + `needs-human` label; `PausedError` → provider rate-limit, work stays at checkpoint; `FaultError` → WIP pushed, resumable, loop stops; `CrossProjectError` → ownership refusal, nothing touched. Epic flow (`epic.go`) stacks sub-issues on an `epic/<ID>-*` integration branch. `lessons.go` keeps an append-only JSONL ledger under `runs/memory/` recalled into later verify/repair prompts.
- **agent** — provider abstraction. `Runner.Run(ctx, prompt, label)` spawns a **fresh subprocess per call, never a resumed session** (that's what keeps verify cold). Providers register via `Spec` in `provider.go`; **nothing branches on provider name outside this package**. `router.go` dispatches per canonical phase key (`build/handoff/verify/repair/bugfix/cleanup/lintfix/commit/pick`); dynamic labels like `verify-retry2` collapse by prefix — new phase labels must keep their canonical prefix or they route to `pick`.
- **tracker** — hybrid direct-API + MCP-via-agent. Linear uses direct GraphQL when `LINEAR_API_KEY` is set, else the tracker's MCP through an agent prompt with sentinel-line parsing. Jira uses direct REST v3; with full REST credentials the tracker is deliberately rest-only (runner nil) so it can never silently switch to the MCP's different Atlassian identity. GitHub issues are MCP-only; PR/CI operations live in `pipeline` shelling to `gh`.
- **state** — per-ticket checkpoints at `.trau/runs/<ID>/state` as sanitized `KEY=value` lines with a phase ranking (building→built→handed_off→verified→pr_open→merged, quarantined=9). **The file format and phase ranking must stay stable across versions.**
- **config** — precedence: defaults < `./trau.ini` < `<repo>/.trau.ini` < `~/.trau.ini` < env (`TRAU_<KEY>` alias wins over bare `KEY`) < CLI flags. Config files are parsed KEY=value, never executed. All knobs are documented in `trau.ini.example`.
- **event** — one JSON line per significant action to `.trau/runs/events.jsonl`; the durable source of truth. The TUI is display-only on top of it.
- **tui** — Bubble Tea **v2** (`charm.land/bubbletea/v2` imports, not `github.com/charmbracelet`). Two modes: `New` (dashboard renderer for headless-launched runs) and `RunSession` (persistent menu shell). The TUI talks to the backend only through the `tui.Actions` interface and never imports pipeline/tracker/agent wiring; the pipeline talks to displays only through `console.Renderer`.
- Supporting: **budget** (spend ceilings), **checks** (pluggable verify checks from `.trau/checks` YAML, executed by the verify agent), **tokens** (normalized per-call token/cost JSONL; "Input" = non-cached portion so totals compare across providers; `CostUSD *float64` nil = unmetered), **usage/probe** (rate-limit windows for the HUD), **vterm** (reconstructs legible screens from raw PTY transcripts), **sanitize**, **doctor**, **notify**, **timelog**, **logger**, **console**.

### Conventions and invariants

- **stdout is byte-stable** — all diagnostics (`--verbose`, `--debug`) go to stderr only; `--json` output must not change.
- Best-effort operations (logging, notify, timelog, gitignore maintenance) must **never abort the loop** — swallow-and-continue is the convention.
- Quarantine is idempotent.
- Tests: table-driven with subtests; hand-rolled fakes shared across the package — `fakeGit`/`fakeRunner`/`fakeTracker`/`newTestPipeline` live in `internal/pipeline/verify_pause_test.go` and other test files embed/extend them. Golden files only in `internal/state/testdata`. No tests hit real providers or trackers; everything goes through the interface seams.
- Architecture decisions live in `docs/adr/`; add an ADR for decisions of that scope.
