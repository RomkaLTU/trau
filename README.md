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

trau can optionally write a per-issue **human-effort** estimate after an issue merges, to
`<repo>/.dev-flow/time/<ID>.json` — the same schema the `dev-flow` skill uses, so a weekly
report or any tool reading `.dev-flow/time/*.json` keeps working when a team moves onto trau.
It is **off by default**: with `TIMELOG_ENABLED=0` (the default) nothing is written and trau
runs exactly as before. The onboarding wizard offers a toggle (defaulting to off), or set the
`TIMELOG_*` keys yourself — see `trau.ini.example`. The number is an **estimate** of senior-dev
effort (a deterministic diffstat heuristic, or a cheap agent call), never the agent's wall-clock.

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

Logs are written under `runs/`:

- `runs/events.jsonl` — the structured event stream for the whole session.
- `runs/<ID>/` — per-phase logs for one issue (`build.log`, `handoff.md`, `verify*.log`, …)
  plus the saved checkpoint, so you can see exactly where an issue stopped.

If an issue gets quarantined (moved to the `needs-human` label), its `runs/<ID>/` directory has
the full trail of what verify rejected.

## License

Apache-2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE). © 2026 Codesomelabs. Architecture
decisions live in `docs/adr/`. External contributions aren't being accepted right now
([CONTRIBUTING.md](CONTRIBUTING.md)).
