# Trau loop

[**trau.sh**](https://trau.sh)

An autonomous, ticket-driven development loop: it pulls the next ready issue from your tracker
and drives it through **build → handoff → verify → commit → PR → CI → merge**, one issue per
iteration, as a single self-contained Go binary. Every phase runs in its own fresh agent
process — so **verify** is a cold, adversarial pass that sees only the handoff brief and the
code on disk, never the build agent's reasoning.

> **Experimental, and it changes code on its own.** Trau edits the target repo, opens PRs, and
> (by default) auto-merges green ones — point it only at a checkout you're willing to let it
> change autonomously. The Claude provider is the exercised path; Codex and Kimi are wired but
> not yet battle-tested.

## Install

```bash
brew install --cask RomkaLTU/trau/trau    # macOS / Linux
trau --version
```

Or from source (Go 1.24+): `git clone https://github.com/RomkaLTU/trau && cd trau && make build`.

Requires `git`, `gh` (authenticated), `jq`, and an agent CLI — `claude` (default), `codex`, or
`kimi` — on `$PATH`, plus a supported issue tracker — Linear, Jira, or GitHub Issues — via its MCP.

## Use

Just run it inside the repo you want it to work on:

```bash
trau
```

That's the whole workflow. On first run an **onboarding wizard** sets up `.trau.ini` for the
repo; after that, `trau` drops you into the **main menu** — a terminal UI where you pick what to
do (preview the next issue, run one issue, run the full loop, check status, and more). No flags
to memorize.

Flags are there when you want them — scripting, CI, one-offs:

```bash
trau --dry-run        # safe: show the next eligible issue, do nothing
trau --once           # process one issue end-to-end, then stop
trau <ID> --once      # a specific issue (treated as an epic if it has sub-issues)
trau --status         # print checkpoints + token/cost totals
```

`trau --help` lists them all. Settings live in `trau.ini` (`cp trau.ini.example trau.ini`, fully
documented). If a run dies mid-issue, just re-run — it resumes from the next unfinished phase.

When the chosen issue has sub-issues and epic flow is enabled, trau treats the parent as an
integration branch: child feature PRs target `epic/<ID>-...`, not `main`. When the epic-scoped
loop stops cleanly, trau checks every direct child; only if all are closed does it open or adopt
the epic-to-`main` PR and mark the parent Done with that PR link.

### Optional time tracking (off by default)

trau can optionally write a per-issue **effort estimate** after an issue merges, as JSON to
`<repo>/.dev-flow/time/<ID>.json` (a format other time-tracking tools can read). It is **off by
default**: with `TIMELOG_ENABLED=0` (the default) nothing is written and trau runs exactly as
before. Enable it from the onboarding wizard (a toggle defaulting to off), or set the
`TIMELOG_*` keys yourself — see `trau.ini.example`. The number is an **estimate** of developer
effort (a deterministic diffstat heuristic, or a cheap agent call), never the agent's wall-clock.

### The web hub (`trau serve`)

`trau serve` starts a local HTTP hub — a versioned JSON API under `/api/v1` and an embedded web
UI at `/` — for watching every trau run on the machine:

```bash
trau serve                       # http://127.0.0.1:8728
```

Because the hub is a window onto an autonomous, merge-capable system, **exposing it is a safety
decision**, and trau enforces an exposure policy:

- **Loopback (the default) is open.** A `127.0.0.1` / `localhost` bind needs no token — nothing
  off the machine can reach it.
- **Any non-loopback bind requires a token.** Set `SERVE_TOKEN` (or `--bind` a routable address
  with one configured). trau **refuses to start** exposed without a token, and every API request
  must then carry it: `Authorization: Bearer <token>`, or the request gets a `401`.

```bash
SERVE_BIND=0.0.0.0 SERVE_TOKEN=$(openssl rand -hex 32) trau serve
curl -H "Authorization: Bearer $SERVE_TOKEN" http://<host>:8728/api/v1/health
```

The **blessed remote path is a private network, not a public port.** Put the host on
[Tailscale](https://tailscale.com) and reach the hub over your tailnet (e.g.
`http://<machine>.<tailnet>.ts.net:8728`) — the token still applies, giving you defence in depth
rather than a single open port to the internet.

## Troubleshooting

Start with the preflight check — it catches the common setup problems before a run can fail
mid-phase:

```bash
trau doctor
```

It verifies `git`, `gh` (installed + authenticated), your agent provider CLI, config sanity,
tracker labels (against the live Linear team when `LINEAR_API_KEY` is set), and write
permissions — reporting each as `✓` / `⚠` / `✗`. It exits non-zero if any required check fails,
so it drops cleanly into CI.

When a run misbehaves, add diagnostics — both go to **stderr only** and never change `stdout`
or `--json` output, so they're safe to leave on in scripts:

```bash
trau --verbose ...   # what the loop is doing (phases, resolved repo/config)
trau --debug ...     # the above plus every git / gh command invoked
```

To watch the agent work in real time: under the TUI, press `w` to flip the activity pane into a
live tail of the running agent's terminal. For a headless or CI run (`--no-tui`, or piped
output), run the read-only counterpart in a second terminal:

```bash
trau watch                       # follow the newest active agent transcript
trau watch --id <stem>           # pin to one transcript (a name under .trau/runs/_agent-results)
trau watch path/to/file.pty.log  # …or an explicit path
```

It tails the live transcript, reconstructs the agent's screen legibly (no raw escape sequences),
follows across phase boundaries, and prints `waiting for agent output…` until a phase starts. It
never touches the loop, so it's safe to start before, during, or after a run.

Logs are written under `.trau/runs/` (override with `RUNS_DIR`). trau adds this path and
`.trau.ini` to the target repo's `.gitignore` on first run, so its artifacts never clutter
`git status`. If you ran an older version that used a root `runs/` dir, trau moves it to
`.trau/runs/` automatically on the next run (unless `runs/` was committed — then move it
yourself):

- `.trau/runs/events.jsonl` — the structured event stream for the whole session.
- `.trau/runs/<ID>/` — per-phase logs for one issue (`build.log`, `handoff.md`, `verify*.log`, …)
  plus the saved checkpoint, so you can see exactly where an issue stopped.

If an issue gets quarantined (moved to the `needs-human` label), its `.trau/runs/<ID>/` directory
has the full trail of what verify rejected.

## License

Apache-2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE). © 2026 Codesomelabs. Architecture
decisions live in `docs/adr/`. External contributions aren't being accepted right now
([CONTRIBUTING.md](CONTRIBUTING.md)).
