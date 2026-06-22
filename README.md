# Trau loop

An autonomous, ticket-driven development loop. It pulls the next ready issue from your tracker
and drives it through a fixed pipeline — **build → handoff → verify → commit → PR → CI → merge**
— one issue per iteration, then picks the next. It ships as a single self-contained Go binary,
`trau`.

The defining property: **every phase runs in its own fresh, isolated agent process** — a new
session, never `--continue`/`--resume`. That keeps **verify** a *cold, adversarial* pass that
can only inherit the durable handoff brief and the code on disk, never the build agent's
reasoning. The agent provider is selected when the loop starts (Claude by default; Codex or
Kimi via `--provider`), and each phase can use a provider-specific model / reasoning effort.

> **Status: experimental.** The Claude path is the primary, exercised one. The Codex and Kimi
> backends are wired behind the same seam but not yet battle-tested. Expect rough edges; issues
> and PRs welcome.

---

## Requirements

- **Go 1.24+** to build (only needed to compile; the resulting binary is standalone).
- `git`, `gh` (authenticated), and `jq` on `$PATH`.
- An agent CLI on `$PATH`: [`claude`](https://docs.claude.com/en/docs/claude-code) (default),
  `codex` (`--provider codex`), or `kimi` (Kimi Code CLI, `--provider kimi`).
- **Linear** + the **Linear MCP** connected to that agent. The loop reaches the tracker *only*
  through the MCP (pick / status / quarantine) — no tracker API key. Linear is
  currently the only supported tracker.
- A **trusted / sandboxed checkout** of the target repo. The loop runs the agent with its
  unattended/skip-permissions flag and, by default, auto-merges green PRs — so point it only at
  a tree you're willing to let it change autonomously.

## Install

```bash
git clone <this-repo> trau && cd trau
make build            # compiles -> bin/trau (single static binary, no CGO)
./bin/trau --version
```

While hacking on the code itself, `go run ./cmd/trau <flags>` compiles and runs in one step.

## Configure

```bash
cp trau.ini.example trau.ini   # then edit; trau.ini is gitignored — never commit it
```

The config is a flat INI subset (`KEY=value` with `#` comments) where every key is
also its environment-variable name — so `.ini` highlights in VS Code and IntelliJ
with no plugin.

`trau.ini.example` documents every knob: `PROVIDER`, `AUTO_MERGE`, `MAX_REPAIRS`,
`MAX_BUGFIXES`, `MAX_ITERATIONS`, `MERGE_METHOD`, `EXPECTED_CHECKS`,
`BROWSER_VERIFY`, `APP_URL`, per-provider models (`CLAUDE_MODEL`/`CLAUDE_EFFORT`,
`CODEX_MODEL`/`CODEX_EFFORT`), phase overrides (`CODEX_BUILD_MODEL`,
`CLAUDE_VERIFY_EFFORT`), and more.

---

## Run it

The loop operates on a **target repo**, resolved first-hit-wins from: `--repo <path>` →
`TRAU_REPO_ROOT` (env / `trau.ini`) → the current directory's git top-level.

```bash
./bin/trau --dry-run                       # SAFE: print the next eligible issue, do nothing
./bin/trau --status                        # SAFE: dump saved checkpoints + token/cost totals
./bin/trau --once                          # one issue end-to-end (real work), then stop
./bin/trau <ID> --once                     # a specific issue / epic (e.g. its sub-issues)
./bin/trau --repo /path/to/app --max 3     # point at a repo, cap iterations
./bin/trau --provider codex --repo /path/to/app  # run this loop on Codex
./bin/trau --reset <ID>                    # discard a stuck attempt, re-queue the issue
```

> **Safety.** Real work is the default; use `--dry-run` for a safe preview. The loop modifies the target repo, opens PRs, and (with `AUTO_MERGE=1`) merges them, so only point it at a checkout you are willing to change autonomously.

> **Issue identifiers.** The loop currently assumes issue IDs use a fixed team prefix
> (`<PREFIX>-<n>`) in its argument form and sentinel matching. This is hard-coded today; making
> the prefix configurable is a known limitation.

### Flags

| Flag | Effect |
| --- | --- |
| `--dry-run` | **Safe.** Ask the tracker for the next pick and exit. No git, no edits, no target repo needed. |
| `--status` | **Safe.** Print `runs/<ID>/state` checkpoints + token/cost totals and exit. |
| `--once` | Process a single issue, then stop. |
| `--parent <ID>` *(or a bare issue ID)* | Treat `<ID>` as an epic if it has sub-issues; otherwise process `<ID>` as a standalone ticket. |
| `--max <N>` | Stop after N issues (default `MAX_ITERATIONS`). |
| `--reset <ID>` | Drop the issue's state + feature branch (local & remote), re-queue it, exit. |
| `--no-resume` | Ignore saved checkpoints; only pick brand-new issues this run. |
| `--provider <name>` | Provider for this run: `claude` (default), `codex`, or `kimi`. |
| `--repo <path>` | Target repo (else `TRAU_REPO_ROOT`, else cwd git top-level). |

### Epic workflow

When you pass a ticket ID, Trau checks whether it has sub-issues. If it does, the ticket is treated as an epic and Trau works on its children using a long-lived integration branch:

1. The first child creates `epic/<id>-<slug>` from `main` and pushes it.
2. Each child feature branch is cut from the epic branch and its PR targets the epic branch.
3. After the first child merges into the epic branch, Trau opens a PR from the epic branch to `main`.
4. The epic PR is never auto-merged — it ships when the epic is complete and a human merges it.

```bash
./bin/trau COD-500 --once                 # auto-detect epic; one child
./bin/trau COD-500 --max 10               # auto-detect epic; up to 10 children
./bin/trau --parent COD-500 --max 10      # explicit epic mode
```

If the ticket has no sub-issues, Trau processes it as a standalone ticket. This keeps half-baked features off `main` and avoids deploying incomplete work.

### What happens per issue

1. **build** — creates the feature branch, moves the issue to *In Progress*, implements the
   change, runs its scoped tests, and stops (no commit).
2. **handoff** — writes a QA brief to `/tmp/handoff-<ID>.md`.
3. **verify** — a *cold* agent re-runs the scoped tests (+ browser checks for UI work when
   `BROWSER_VERIFY` is on) and writes `/tmp/verify-<ID>.json` (`{pass, summary, failures}`). On
   `pass=false` it **self-heals**: a fresh agent fixes in-scope defects and re-verifies, up to
   `MAX_REPAIRS`. If quick repairs fail, a dedicated **bugfix** agent takes over and tries to
   fix all remaining QA issues comprehensively, up to `MAX_BUGFIXES`.
4. **commit → push → PR** — commits, pushes, opens a PR against the base, issue → *In Review*.
5. **CI → merge** — waits for required checks, then (if `AUTO_MERGE=1`) squash-merges, deletes
   the branch, marks the issue *Done*.

A failure never aborts the loop: the attempt is preserved on its branch and the loop continues
to the next issue. When verify can't be healed, the loop files a last-resort **HITL blocker**
issue, quarantines the original ticket (drops the ready label, adds a needs-human label), and
moves on.

### Where things land

- `runs/<ID>/state` — durable checkpoint (gitignored, survives reboots).
- `runs/<ID>/<phase>.log` — per-phase agent transcript.
- `runs/<ID>/tokens.jsonl` — one normalized token/cost line per agent call (summed by `--status`).
- `runs/events.jsonl` — machine-readable structured event stream.
- `/tmp/handoff-<ID>.md`, `/tmp/verify-<ID>.json` — handoff brief + QA verdict.
- Everything else (issue status, PR links, and last-resort HITL blockers) lives in the tracker.

### Crash / resume

If a run dies mid-issue (network drop, API error, kill), **just re-run the same command.** The
loop reads `runs/<ID>/state`, re-checks-out the feature branch, and continues from the **next**
unfinished phase — completed phases are not redone, and an already-merged PR is reconciled, not
re-merged. The checkpoint advances through:

```
building → built → handed_off → verified → pr_open → merged   (quarantined = terminal)
```

An issue whose state file was lost is still adopted: if HEAD is parked on a `feature/<ID>`
branch, the checkpoint is inferred from on-disk artifacts. An issue stuck beyond repair:
`--reset <ID>` throws the attempt away and starts over.

---

## Provider Models

Choose the provider once per loop with `--provider <name>` or `PROVIDER=<name>`. Trau then uses
the selected provider's defaults and phase-specific model/effort settings. This keeps one
`trau.ini` usable for concurrent provider runs:

```bash
./bin/trau --provider codex --repo /work/app-a
./bin/trau --provider claude --repo /work/app-b
```

Provider defaults:

```bash
CLAUDE_MODEL=claude-sonnet-4-6
CLAUDE_EFFORT=high

CODEX_MODEL=gpt-5.4-mini
CODEX_EFFORT=medium
```

Phase overrides for the selected provider:

```bash
CODEX_BUILD_MODEL=gpt-5.5
CODEX_BUILD_EFFORT=xhigh
CODEX_REPAIR_MODEL=gpt-5.5
CODEX_REPAIR_EFFORT=high
CODEX_COMMIT_MODEL=gpt-5.4-mini
CODEX_COMMIT_EFFORT=low

CLAUDE_BUILD_MODEL=claude-opus-4-8
CLAUDE_BUILD_EFFORT=max
CLAUDE_VERIFY_MODEL=claude-sonnet-4-6
CLAUDE_VERIFY_EFFORT=medium
```

Each call records the provider/model/effort that ran it in `runs/events.jsonl` and the live
stat line.

## Console output

The terminal stream is colored and TTY-aware: a `▸ phase` marker when a phase starts, a dim
`↳ turns · tokens · time` recap when it finishes, and a grand total at the end. Raw events go to
`runs/events.jsonl`, not the screen.

| Env | Effect |
| --- | --- |
| `NO_COLOR=1` | Disable color (also auto-off when piped/redirected). |
| `CLICOLOR_FORCE=1` | Force color even when piped (e.g. into `less -R`). |
| `TRAU_LOG_JSON=1` | Stream raw JSON events to stderr (for `\| jq` / a collector). |

Piped/redirected output is plain, so `--status` and dry-run output are stable for scripting.

---

## Develop

```bash
make fmt      # gofmt -w .
make vet      # go vet ./...
make lint     # golangci-lint run
make test     # compile/race check; Go tests are intentionally absent for now
make build    # -> bin/trau
```

Run the current gates before opening a PR: `make fmt vet lint build`. The Go test suite is
intentionally paused while the main loop behavior stabilizes; tests will be added back once the
core functionality settles.

- **Entrypoint:** `cmd/trau`. **Packages:** `internal/{config,agent,pipeline,state,tracker,tokens,event,console}`.
- **Agent seam:** `internal/agent` is the single dispatch point and the *only* place provider
  divergence lives (invocation flags, model, token normalization). Adding a provider =
  implementing the `Runner` once; nothing in phase logic branches on the provider. Phase
  ceremony (branch/handoff/verify/commit) is baked into trau's own prompts — no external skill
  dependency; the build agent only auto-loads the target repo's *domain* skills.
- **Isolation is load-bearing:** each phase is a fresh process. Don't add session sharing
  between phases — it's what keeps verify cold.
- **No manager layer:** no retry framework, session manager, daemon supervisor, or config
  system. The small surface is deliberate.

See `docs/adr/` for the architecture decision records.

## Contributing

**We are not accepting external contributions at this time.** Trau is published so others can
use, study, and fork it, but incoming pull requests will be closed and there is no CLA. You're
welcome to fork it or open an issue. See [CONTRIBUTING.md](CONTRIBUTING.md) for details.

## License

Licensed under the Apache License, Version 2.0 — see [LICENSE](LICENSE). Copyright © 2026
Codesomelabs. Third-party attributions are listed in [NOTICE](NOTICE).
