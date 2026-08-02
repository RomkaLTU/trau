# Prompt receipts

Every prohibition in the prompt registry (`internal/prompts/registry.go`) costs tokens
on every run that carries it, and prompts only grow: each incident adds a clause, and
nothing ever removes one. This file records *why* each prohibition is there, so a
prompt-diet discussion can argue about the evidence instead of re-deriving it.

A **receipt** is the concrete reason a rule exists: an incident it was written in
response to, or a product rule it enforces. A rule with a receipt earns its tokens
until the receipt is retired. A rule without one is a guess.

## Receipts

| Prohibition / block | Prompt(s) | Receipt | Status |
| --- | --- | --- | --- |
| Browser-honesty block — "Account for browser QA honestly… never skip a UI surface out of concern for a user's session" | `verify` | Observed verify cheating: verifiers reported UI slices as verified without driving the browser, some of them declining on the invented grounds that they might disturb the user's session. | Receipted (incident) |
| Only-relevant-tests — "run only the tests relevant to this slice… not the entire suite" | `build`, `verify` | Build-timeout spin: whole-suite runs hit `AGENT_TIMEOUT` and burned ~14M tokens per attempt without ever finishing the slice. | Receipted (incident) |
| REFUSED sentinel — "If the ticket clearly belongs to a DIFFERENT repository… end your reply with `REFUSED: …`" | `build` | M4C-64 cwd-escape: a build wandered into a sibling repository, made its changes there, and delivered an empty managed diff. | Receipted (incident) |
| AI-authorship ban — "no mention of the loop or of any AI agent"; "do NOT add any 'Co-authored-by'… or any mention of AI/assistant authorship" | `build`, `commit`, `ci_repair`, `epic_repair` | Operator rule: trau's own repository and everything it publishes (commits, PR bodies) carry no AI signals. | Receipted (product rule) |
| Never-AskUserQuestion — "Never call AskUserQuestion or wait for a reply" | `preamble`, `explore_preamble` | Unattended runs: no human is attached to the phase, so a question is an indefinite stall that burns the agent timeout. | Receipted (product rule) |
| No commit/push inside a phase — "Do not commit, push, or open a PR" | `build`, `handoff`, `verify`, `cleanup`, `lint_fix`, `resolve_conflicts`, `timelog_estimate` | Phase-boundary protocol: the loop owns commit, push, PR, and tracker writes, so a phase that half-delivers cannot leave the run in a state no checkpoint describes. | Receipted (product rule) |
| Artifact-contract prose — "write ONLY … to exactly `<path>` (overwrite if present) and nowhere else" | `handoff`, `verify`, `rubric`, `build_notes`, `timelog_estimate` | Receipted by observed compliance: 0 misses across 329 final verdicts. An MCP `report_*` tool as a structured alternative was evaluated and rejected (D-grill, 2026-07-25) — the prose contract already achieves what the tool would enforce, at lower cost. | Receipted (monitored — COD-1200's `verdict_missing` event tracks any future miss) |
| `code_style` block — "Write it the way a senior engineer on this project would… Skip the AI tells" | `code_style` (appended to `build`, `repair`, `bugfix`, `push_repair`) | None. Written from intuition, never traced to an incident, and never measured against a run without it. | **Unreceipted — under experiment** (CODE_STYLE_NOTE A/B) |

## Maintenance rule

**A new prohibition arrives with its receipt.** Name the incident it answers or the
product rule it enforces, in the same change that adds the clause, and add the row
here. A clause proposed without one is not rejected outright — it is an *experiment
candidate by default*: it lands behind a flag with an A/B that measures whether runs
carrying it differ from runs that do not, and it either earns a receipt from that
result or comes back out.

The same applies in reverse: when a receipt stops holding — the incident class is
fixed structurally, or the measurement shows no effect — the clause is a candidate for
removal, not a permanent fixture.
