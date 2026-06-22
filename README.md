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

## License

Apache-2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE). © 2026 Codesomelabs. Architecture
decisions live in `docs/adr/`. External contributions aren't being accepted right now
([CONTRIBUTING.md](CONTRIBUTING.md)).
