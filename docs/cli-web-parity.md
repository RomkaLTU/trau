# CLI ↔ web parity map

Every trau CLI/TUI operation, and the web-hub (`trau serve`) surface that covers
it — or the named gap where none does. The web hub is a **window onto** the same
autonomous loop, not a second implementation: it shells the hub's own binary for
every run and reads the same `.trau/runs/` event stream the TUI reads, so the two
never diverge on behavior, only on ergonomics.

Verified against the shipped web-wiring slices COD-736–745 (Overview, Loop, Run
once, Runs board + detail, Terminal, Costs, Lessons, Settings) on
2026-07-05. Re-verify this map whenever a screen's actions change.

Web locations are given as **Screen → control**; routes are shown where they
help. Screen names match the sidebar nav (OPERATE · OBSERVE · CONFIGURE).

## Run and loop

| CLI / TUI | Web surface |
| --- | --- |
| `trau` — bare loop: resume any in-flight ticket, else pick the next ready one | **Loop** → scope *Ready queue* → **Start loop**. A loop started here keeps running across a hub restart (supervisor-owned), and **Overview → Live loops** shows it grazing the queue. |
| `trau <ID>` on an epic · `trau --parent <ID>` · bare `<PREFIX>-<n>` epic | **Loop** → scope *Epic* → epic id → **Start loop**. The epic preview lists sub-issues and their state before you commit. |
| `trau <ID> --once` · `trau --once` | **Loop** → **Run next** — pick from the ready queue or add by id (fetch-then-confirm). The ticket joins the front of the Queue and the drain arms; the Queue is the web's only start path (ADR 0015). |
| `trau --max <N>` (cap iterations for this run) | **Loop** → *max iterations* stepper. |
| `trau --no-resume` (skip the resume scan) | **Loop** → *skip resume* toggle. |
| `trau --provider <name>` (Provider override) | **Loop** → per-queue-item *Provider override*. Ephemeral, single-run, reverts to config after — matches the CLI flag exactly. |
| `trau --repo <path>` (target repo) | *Repo picker* on every screen. The web only lists **allowlisted** repos; it can never launch a loop somewhere the operator hasn't sanctioned. |

## Preview and list

| CLI / TUI | Web surface |
| --- | --- |
| `trau --dry-run` — print the next eligible ticket, do nothing | **Loop** *Ready queue* preview (the read-only dry-run endpoint remains). |
| `trau --list-eligible` | **Loop** *Ready queue* preview. |
| `trau --list-epic <ID>` | **Loop** *Epic* scope sub-issue preview. |

## Status and checkpoints

| CLI / TUI | Web surface |
| --- | --- |
| `trau --status` (checkpoints + token/cost totals; auto-reconciles stale rows) | **Overview** (spend-today · active-loops · needs-attention tiles + the attention queue) **and Runs** — the board of every tracked run grouped by pipeline phase. |
| TUI *Status* screen · per-ticket checkpoint inspection | **Runs** board → **Run detail** (`/runs/<repo>/<ticket>`): phase costs, verify verdict, rubric, handoff brief. |

## Reset · clear · reconcile

Web surface: **Runs** row overflow menu (*⋯*) and the **Run detail** action row.
Each opens a confirm dialog; a live loop holding the repo answers with a
plain-language refusal rather than a raw error.

| CLI / TUI | Web surface |
| --- | --- |
| `trau --reset <ID>` (drop branch + state, re-queue) | *Reset* → confirm dialog. |
| `trau --reset <ID> --force` (reset an already-merged ticket) | *Reset* on a `merged` ticket → a **type-to-confirm** force dialog (auto-required for merged; also raised when the API asks for force). |
| `trau --clear <ID>` (a.k.a. `--forget`) — drop the local checkpoint only | *Clear* → confirm dialog. Git and the tracker are left untouched. |
| reconcile (folded into `--status`; TUI *Status* `r`) | *Reconcile* → cross-checks every in-flight checkpoint against the tracker and drops any already Done/Canceled. |

## Watch and logs

| CLI / TUI | Web surface |
| --- | --- |
| `trau watch` · `trau watch --id <stem>` · `trau watch <path>` (headless transcript tail) | **Terminal** — live phase transcripts; follow-newest or pin one transcript, per repo. |
| TUI `w` (flip the activity pane into a live agent tail) | **Live run view** (`/live/<repo>/<ticket>`): embedded Terminal + the activity feed + phase stepper. |
| TUI *Logs* (per-ticket phase logs) · lessons ledger | **Run detail** for one run; **Lessons** for the accumulated ledger. |

## Stop

| CLI / TUI | Web surface |
| --- | --- |
| TUI `q` / Ctrl-C — quit/stop the running loop | **Loop** *Stop loop* · **Overview** loop-card *Stop* · **Run detail** *Stop*. All graceful: the current ticket finishes its checkpoint, then the loop stops; work in progress is preserved. |
| (resume a paused/faulted run — CLI does this by re-running `trau`) | **Run detail** *Resume* — Run next on the checkpointed ticket: it joins the front of the Queue and the drain relaunches it. |

## Settings

| CLI / TUI | Web surface |
| --- | --- |
| TUI *Settings* (edit `.ini`) · the layered config precedence | **Settings** — layered config resolved project → user → default; edit any key and choose which layer the write lands in. |

## Onboarding

| CLI / TUI | Web surface |
| --- | --- |
| Onboarding wizard (first-run `.trau.ini` setup; TUI *Re-run onboarding*) | **Add a project** (`/projects/new`, reached from the sidebar CONFIGURE group and the repo switcher's *Add a project* row) — a path → inspect → tracker → essentials → seed-sync → done wizard that registers the repo, writes the tracker and essentials config to the project layer, and seeds the issue store. Secrets are write-only (blank keeps the stored value); the tracker step is gated on a passing connection test. Registration stays fail-closed: on an exposed bind it also needs `SERVE_ALLOW_REGISTER` (the 403 renders an in-app remediation callout, never a raw error). See ADR 0012. |

## Costs and lessons

| CLI / TUI | Web surface |
| --- | --- |
| token/cost totals from `trau --status` | **Costs** — spend timeseries with window + group-by and anomaly flags. |
| the lessons ledger under `.trau/runs/memory/` | **Lessons** — browse and search what the agent learned across runs. |

## Serve

`trau serve` is not mapped — it *is* the hub every surface above renders inside.
Its exposure policy (loopback open, any routable bind requires `SERVE_TOKEN`, and
repo (un)registration on such a bind additionally requires `SERVE_ALLOW_REGISTER`)
is a `serve`-only concern with no in-app control.

## Deliberate gaps

These CLI/TUI operations have **no web surface, by design** — declared here so a
gap is a decision, not a surprise.

| CLI / TUI | Why it stays terminal-only |
| --- | --- |
| `trau doctor` (preflight: git/gh/provider/config/labels/write perms) | An exit-code-driven check meant to run before a loop and to drop into CI. It diagnoses the *machine*, which a browser tab can't act on, and predates having a hub to serve. |
| Interactive planning flow (TUI *Plan*: raw idea → Q&A rounds → PRD → slices) | The web ships only the terminal publish step (Author → PRD), not the multi-round decomposition into an epic + slices. |
| `--verbose` · `--debug` · `--no-tui` · `--json` · `--yes` · `-v/--version` | Shell/scripting/CI diagnostics with no browser analog. The hub's build version rides the `/api/v1/health` response rather than a screen. |
