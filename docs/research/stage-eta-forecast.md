# Stage ETA forecast — can we show a viewer "time left"?

A viewer watching a run today sees only elapsed time: `elapsed / in phase` on the
run view and Loop screen, row age in the ledger, per-step timers in the TUI. This
is a research spike into whether that can honestly become **approximate time left**
per display Step (Build / Verify / Ship), and if so, how. No product code, schema,
config or ADR file is changed by this ticket — the ADR amendment below is a sketch
only, and the slice breakdown at the end is a proposal, not filed work.

**Method.** Every number here comes from a **read-only snapshot** of the live hub DB
(`~/.trau/trau.db`, 29.7 MB) copied to a temp dir on **2026-07-26 03:23** and opened
with `mode=ro`; the live DB and hub were never written to, locked, moved or
restarted. Runs were reconstructed by replaying `activity_change` deltas with the
same segmentation `runStepDurations()` uses (`internal/webserver/durations.go:90`),
and every model was backtested **walk-forward** — at each Step entry a model may
only see runs that had already *terminated* before that instant.

**Corpus.** 168 completed runs, 2026-07-18 → 2026-07-26 (7.5 days): loop 139,
trucknet 12, trau-promo 8, melga 8, one scratch repo. Terminal states: merged 155,
faulted 5, paused 4, quarantined 2, stopped 2.

Baseline distributions the whole document refers back to:

| Step | n | P10 | P50 | mean | P90 | max |
|---|---|---|---|---|---|---|
| Build | 166 | 4m55s | 13m53s | 14m31s | 25m13s | 47m03s |
| Verify | 161 | 4m57s | 12m31s | 19m57s | 48m04s | 91m48s |
| Ship | 157 | — | 0m13s | 0m38s | 1m20s | 7m11s |
| whole run | 168 | — | 28m48s | 34m03s | 64m45s | — |

---

## 1. Data source

Four candidate sources exist. Only one of them can time a whole run, and it is the
shallowest.

| Source | Depth in snapshot | Runs / tickets | Pruned? | Times what |
|---|---|---|---|---|
| `events` kind=`activity_change` | 7.4 days | 168 runs | yes, 5000 rows/repo | **everything**, incl. CI/merge wait |
| `phase_logs` | 36.5 days | 402 tickets | **never** | phase *completions* only |
| `events` kind=`agent_call` (`duration_ms` in fields) | same pool | 1,210 calls | yes, same 5000 | agent calls only |
| `token_calls.duration_ms` | 2.2 days | 69 tickets | yes, 5000 rows/repo | agent calls only |

### The retention ceiling is set by unrelated traffic

The loop repo sits at **exactly 5000 event rows** — the cap
(`internal/config/config.go:475`, pruned per-repo by row rank hourly,
`internal/hubstore/retention.go:27`). That is 36 event rows per run and a **7.4-day**
window. But `activity_change` is only **26%** of that budget:

| kind | rows | share of the 5000-row cap |
|---|---|---|
| `usage_window` | 1677 | 34% |
| `activity_change` | 1277 | 26% |
| `agent_start` | 784 | 16% |
| `agent_call` | 768 | 15% |
| `state_change` | 139 | 3% |
| everything else | 355 | 7% |

**74% of the retention budget is spent on kinds an ETA never reads**, and the two
kinds it does need (`activity_change` + `state_change`, 29%) age out at the pace the
*other* 71% is written. Any future event kind shortens the ETA's memory without
anyone noticing. This — not accuracy — is the strongest argument in §5.

### `phase_logs` is deep, exact for Verify, and blind to Build

`phase_logs` writes one row per (ticket, phase) at phase **completion**
(`internal/pipeline/pipeline.go:2959`, after the agent returns; `updated_at` stamped
in unix-ns at the hub, `internal/hubstore/phaselogs.go:44`). Consecutive completion
timestamps therefore delimit the *next* phase, and they line up with
`activity_change` to the second. Comparing phase_logs-derived Step durations against
the activity_change ground truth on the 153 overlapping tickets:

| Step | n | median bias | median abs rel. diff | P90 abs rel. diff |
|---|---|---|---|---|
| Verify | 152 | **+0s** | **0%** | 11% |
| Build | 147 | −755s | 89% | 94% |
| Ship | 1 | −26s | 44% | 44% |

Verify is **exact** — repair/bugfix/verify-retry each carry a distinct phase label
(`repair1`, `verify-retry1`, …, `internal/pipeline/pipeline.go:1889`), so the whole
attempt loop is delimited. Build is not: phase_logs records only *completions* and
there is no run-start row, so the build agent call itself — p50 **11m46s**, the
single largest span in a run — is structurally unmeasurable. Ship is worse: `pr`,
`ci-wait`, `merge` and `merge-wait` write no phase log at all, and 294/402 tickets
(73%) yield zero Ship time.

Two further caveats: rows are overwritten on rerun (`ON CONFLICT … DO UPDATE`,
`internal/hubstore/phaselogs.go:52`) so only the last run of a ticket survives and
runs cannot be told apart; and `clearPhaseLogs` wipes them on fresh-build/reset.

What phase_logs *does* buy is depth: **244 tickets** are timed by phase_logs entirely
outside the `activity_change` window — 412 Verify-capable samples against 168, a
**2.5×** deeper Verify baseline reaching back 36 days.

### The agent-call sources miss less than expected — but are the wrong shape

`token_calls.duration_ms` has no backfill (migration 0032) and in this snapshot
covers only **366 of 1,699 rows (22%)** across 2.2 days. `agent_call` events carry the
same `duration_ms` in their fields and are richer (1,210 calls) but live in the same
capped pool.

Both are agent wall-clock, so they miss `ci-wait`, `merge`, `merge-wait` and `pr`.
That gap is smaller than the ticket assumed: agent-call activities account for
**99%** of median run wall-clock (28m26s of 28m48s) under the default `AUTO_MERGE=1`.
The disqualifier is shape, not coverage — a per-call duration cannot tell you *how
many calls remain*, which (§2) is the entire forecasting problem for Verify.

### Precision footnote

`events.ts` is RFC3339 at **second** resolution with no sub-second component. Every
duration derived from it is ±1s per boundary. That is immaterial for Build and
Verify (minutes) but it is why `handoff` shows 135/135 zero-length spans and why 96%
of `ci-wait` spans measure zero.

### Verdict — RQ1

**A combination, weighted to `activity_change`.** It is the only source that times a
whole run and it is already the canonical one (ADR 0009). Use `phase_logs` for one
purpose only: a **one-time backfill of the Verify baseline**, where it agrees exactly
and reaches 2.5× further back. Do not use it for Build (blind to the build call) or
Ship (blind to the whole Step). Do not build on `token_calls`/`agent_call` durations —
99% coverage of wall-clock does not compensate for being unable to express
"how many verify attempts are left".

---

## 2. Model

### Can Verify be forecast deterministically? **No.**

Verify is a loop, not a session (`internal/pipeline/pipeline.go:1870`): verify → up
to `MaxRepairs`(2) repair+re-verify → up to `MaxBugfixes`(2) bugfix+re-verify, plus an
optional multi-member panel (`fanOutPanel:4245`) and a browser-verify gate that can
fire one more attempt (`:2000`). Worst case is 9 agent calls, or `(5 × N) + 4` with an
N-member panel. The observed attempt distribution:

| verify attempts | runs | share | Verify Step P50 |
|---|---|---|---|
| 1 | 115 | 68% | 9m34s |
| 2 | 44 | 26% | 29m10s |
| 3 | 8 | 5% | 62m25s |
| 5 | 1 | 0.6% | 91m48s |

One extra attempt roughly **triples** the Step. Whether it happens is decided by an
adversarial verifier's verdict on code that does not exist yet at the moment you'd
have to make the prediction. So the attempt count is not knowable in advance, and
Verify's duration is a distribution, not a value.

The **structural** alternative — quoting the loop's own bounds, "attempt 2 of 5, each
capped by `AGENT_TIMEOUT`(3600s)" — is deterministic and useless. The bound is
9 × 3600s = **540m** against an observed P50 of 12m31s and a max of 91m48s: **43× the
median**, 11× the P90, and 5.9× the single worst run ever recorded. **No run in the
corpus reached even 25% of the bound.** Build's bound is 4× its median. A number
that is never within an order of magnitude of the truth is not a forecast.

The best achievable alternative is a **statistical estimate with explicit error
bars**, quantified in §3 and phrased in §4. Its honest error bars are: Verify median
absolute error ≈ **6m30s**, median absolute percentage error ≈ **59%**, P90 absolute
error ≈ **36m29s**.

### Per-repo vs global

Per-repo baselines **did not beat global** in backtest for Build or Verify (§3) —
because the global pool is 83% loop runs, so "global" already *is* the loop repo's
baseline, while the smaller repos never accumulate enough samples inside the 7.4-day
window to beat it. Per-repo *does* win decisively for Ship, where repos genuinely
differ (loop MdAPE 8% — it merges instantly; trucknet 92%, melga 95% — they wait on
CI).

### Median vs mean vs percentile

Median wins everywhere it was tested. Both distributions are right-skewed (Verify
mean 19m57s vs median 12m31s), so the mean is dragged by repair loops: global mean
costs +4m21s MdAE on Verify against global median. Higher percentiles are worse as
*point* estimates by construction (global P70 is the worst Verify model at 13m48s
MdAE) but are the right tool for *phrasing* — see §4.

### Config-cohort (`config_hash`) awareness

**No measurable improvement.** Cohort-aware medians scored 5m08s MdAE on Build
against 5m10s for the plain global median — a 2-second difference on a 5-minute
error — and were *worse* on Verify (8m36s vs 6m30s). Coverage is the reason: only
71/168 runs (42%) have a `config_hash` at all, across 7 cohorts of which 3 hold
fewer than 8 tickets, so the model spends most of its time falling back. The
existing cohort analytics (`internal/hubstore/cohorts.go:93`, `avg_duration_ms` at
`internal/webserver/cohorts.go:205`) remain the right tool for cost attribution;
they are not a useful ETA feature at this data volume.

### Attempt-adaptive Verify

Re-estimating *remaining* Verify time at each attempt boundary is the model the
ticket expected to win. It does not — on accuracy:

| at verify attempt | n | attempt-adaptive MdAE / P90 | static Verify median MdAE / P90 |
|---|---|---|---|
| 1 | 141 | 6m30s / 36m29s | 6m30s / 36m29s |
| 2 | 48 | 5m01s / 20m00s | 4m50s / 21m10s |
| 3 | 3 | 5m09s / 10m28s | 2m39s / 5m46s |
| **pooled** | **192** | **6m08s / 35m40s (MdAPE 54%)** | **6m06s / 35m55s (MdAPE 53%)** |

The reason is the finding underneath it: **remaining Verify time is close to
memoryless.** Median remaining is 12m31s at attempt 1, 13m53s at attempt 2, 13m17s at
attempt 3 — knowing you are deep in the repair loop barely changes how much longer
it will take, because escalation probability decays (P(≥2 | at 1) = 0.315,
P(≥3 | at 2) = 0.170, P(≥4 | at 3) = 0.111) roughly as fast as the remaining work
grows.

Adopt attempt-adaptivity anyway, for a different reason: **re-anchoring**. Without
it, a run on attempt 2 has already spent more than the whole-Step estimate, so any
displayed figure is stale or exhausted (§4). Re-quoting at each `repair`/`bugfix`
`activity_change` keeps the number live. Its value is honesty, not accuracy — and it
should be justified that way rather than as a precision win.

### Features that were tested and rejected

- **Trailing window** (last 20 runs per repo) was worse than the full history on
  both Steps (Build 5m46s vs 5m20s; Verify 8m29s vs 8m16s). Stage durations are not
  drifting fast enough to justify discarding history.
- **Build time as a Verify feature**: r = 0.328, r² = 0.107. Conditioning Verify on
  the ticket's own Build tercile improves MdAPE from 59% to **55%** — real but small.
  Worth revisiting only after the baseline exists.

### Verdict — RQ2

Global median per Step, **except Ship which must be per-repo**; no cohort awareness;
attempt-adaptive re-anchoring for Verify adopted for staleness, not accuracy. Verify
is not deterministically forecastable and no amount of modelling changes that — the
attempt count is the dominant term and it is decided after the prediction is made.

---

## 3. Backtest

Walk-forward over the 168 runs, 20-run warm-up, prediction made at Step entry using
only runs terminated before that instant. `MdAE` = median absolute error,
`MdAPE` = median absolute percentage error.

**Build** (n = 146 predictions)

| model | MdAE | P90 AE | MdAPE | P90 APE |
|---|---|---|---|---|
| per-repo + cohort median | 5m08s | 12m39s | 35% | 184% |
| **global median** | **5m10s** | **12m07s** | **34%** | **162%** |
| global mean | 5m15s | 12m50s | 37% | 176% |
| per-repo median | 5m20s | 12m41s | 38% | 176% |
| per-repo median (last 20) | 5m46s | 12m38s | 38% | 183% |
| global P70 | 6m35s | 14m09s | 39% | 247% |

**Verify** (n = 141)

| model | MdAE | P90 AE | MdAPE | P90 APE |
|---|---|---|---|---|
| **global median** | **6m30s** | **36m29s** | **59%** | **155%** |
| per-repo median | 8m16s | 34m45s | 61% | 182% |
| per-repo median (last 20) | 8m29s | 36m08s | 63% | 213% |
| per-repo + cohort median | 8m36s | 36m34s | 59% | 198% |
| global mean | 10m51s | 29m47s | 67% | 263% |
| global P70 | 13m48s | 26m42s | 81% | 341% |

**Ship** (n = 137)

| model | MdAE | P90 AE | MdAPE | P90 APE |
|---|---|---|---|---|
| **per-repo median** | **0m02s** | **1m18s** | **12%** | **48%** |
| global median | 0m02s | 1m36s | 12% | 87% |
| global mean | 0m19s | 1m31s | 124% | 222% |

### Per repo (global-median model)

| repo | Step | n | MdAE | P90 AE | MdAPE |
|---|---|---|---|---|---|
| loop | Build | 126 | 5m04s | 12m02s | 33% |
| trucknet | Build | 11 | 6m26s | 11m24s | 53% |
| melga | Build | 8 | 7m57s | 16m24s | 89% |
| loop | Verify | 122 | 6m25s | 36m28s | 55% |
| trucknet | Verify | 11 | 6m19s | 16m59s | 82% |
| melga | Verify | 8 | 9m45s | 37m47s | 69% |
| loop | Ship | 119 | 0m01s | 0m03s | 8% |
| trucknet | Ship | 11 | 2m22s | 4m56s | 92% |
| melga | Ship | 7 | 4m09s | 6m38s | 95% |

Build is forecastable to roughly a third of its own length. Verify is forecastable to
roughly 60% of its own length — usable as a hedged statement, not as a countdown.
Ship under `AUTO_MERGE=1` is essentially a constant (P50 13s; only 17/168 runs, 10%,
exceed 60s; `ci-wait` actually waited in 7 runs, P50 3m40s) and is not worth
forecasting at all until it is actually entered.

### How much history is enough

Accuracy is **flat** in history depth — the binding constraint is variance, not
sample count:

| Step | ≥5 prior | ≥10 | ≥20 | ≥40 | ≥80 |
|---|---|---|---|---|---|
| Build MdAPE | 36% | 34% | 34% | 34% | 41% |
| Verify MdAPE | 58% | 59% | 59% | 58% | 61% |

Five samples buy essentially all the accuracy 80 samples do. This matters for §5:
a deeper store improves **availability and cold-start**, not precision.

### Cold start — a repo with no history of its own

Predicting a repo's runs using only *other* repos' history:

| repo | Step | n | fallback MdAE / MdAPE | own-history MdAE / MdAPE |
|---|---|---|---|---|
| trucknet | Build | 11 | 6m30s / 53% | 4m59s / 56% |
| melga | Build | 8 | 7m58s / 89% | 12m08s / 67% |
| trucknet | Verify | 11 | 6m40s / 82% | 4m32s / 58% |
| melga | Verify | 8 | 9m44s / 77% | 23m07s / 74% |
| trucknet | Ship | 11 | 2m22s / 92% | 1m44s / 54% |
| melga | Ship | 7 | 4m09s / 95% | 2m36s / 38% |

Degraded but bounded for Build and Verify — a cross-repo fallback is defensible if
the copy hedges it. For Ship it is not: 92–95% MdAPE, because the fallback pool is
loop's instant-merge behaviour and these repos genuinely wait.

---

## 4. Presentation

Three candidate phrasings, judged against the measured spread.

### Soft point estimate ("~12m left") — **reject**

A median point estimate is overrun in **~51% of runs by construction**, and the
overshoot is not small:

| Step | overrun rate | median overshoot | P90 overshoot |
|---|---|---|---|
| Build | 74/146 = 51% | 1.38× | 2.16× |
| Verify | 72/141 = 51% | **2.15×** | **4.95×** |
| Ship | 68/137 = 50% | 1.19× | 19.94× |

Half of all viewers would watch a Verify countdown reach zero and then sit there for
another 13 minutes at the median. A single number implies a precision the data
(MdAPE 59%) does not have.

### Range ("8–20m") — **reject**

Empirical quantile intervals are honestly calibrated but unusably wide for Verify:

| Step | interval | nominal | observed coverage | median width |
|---|---|---|---|---|
| Build | P25–P75 | 50% | 47% | 9m37s |
| Build | P20–P80 | 60% | 58% | 11m51s |
| Build | P10–P90 | 80% | 75% | 19m33s |
| Verify | P25–P75 | 50% | 48% | 17m21s |
| Verify | P20–P80 | 60% | 57% | 21m08s |
| Verify | P10–P90 | 80% | 72% | **41m44s** |

To cover 72% of Verify outcomes the range has to read "**8m – 50m**". That is a true
statement that helps nobody decide whether to wait. Narrowing it to P25–P75 buys
readability at 48% coverage — wrong more often than right.

### Percentile phrasing ("usually done within 15m") — **recommend**

One-sided P80, per Step, is the best-calibrated option and the only one whose failure
mode is graceful:

| Step | quantile | observed hit rate | median quoted value |
|---|---|---|---|
| Build | P70 | 70% | 17m47s |
| Build | **P80** | **77%** | **19m51s** |
| Build | P90 | 85% | 24m41s |
| Verify | P70 | 68% | 23m08s |
| Verify | **P80** | **77%** | **27m58s** |
| Verify | P90 | 84% | 46m50s |

P80 lands at 77% observed for both Steps — a 3-point optimism, which is honest
enough to say out loud. P90 is better calibrated still but quotes 46m50s for Verify,
which is so conservative it stops being information.

### Required behaviours

**When elapsed exceeds the estimate.** This happens in **23% of runs** at P80 (34/146
Build, 33/141 Verify) — by design, that is what P80 means. The overrun is mild
(Build median 1.26×, P90 1.78×; Verify median 1.73×, P90 2.90×). The display must
**never** count down, never show a negative, and never sit at zero: once elapsed
passes the quoted bound, replace the quote with a plain "taking longer than usual"
and keep showing the existing elapsed timer. No second estimate — the run has
already left the distribution the estimate came from.

**Re-anchoring.** Re-quote at every `repair`/`bugfix` `activity_change`, using the
attempt-conditional remaining-time baseline (§2). This is what keeps a run on
attempt 2 from displaying a permanently-exhausted Build-era number. Note that
`state_since` is the wrong clock to drive this from: it resets on every activity
**or detail** change (`internal/hubpresence/hubpresence.go:86,111`), so each repair
attempt zeroes it. The TUI already computes true Step-entry time correctly
(`internal/tui/steps.go:70` keeps `start` across intra-Step activity changes); the web
would need the same, either client-side or served.

**Sparse or no history.** Three tiers: **≥5 runs for the repo** → repo baseline
(§3 shows 5 samples is enough); **<5 for the repo but ≥20 across repos** → cross-repo
fallback with hedged copy ("usually ~14m across repos"), which §3 shows is degraded
but bounded for Build and Verify; **<20 globally, or Ship on any repo without its own
history** → show nothing. Showing nothing is a real answer — `runStepDurations`
already sets this precedent by returning nil for runs predating the signal
(`internal/webserver/durations.go:52`).

**Ship.** Do not forecast it under `AUTO_MERGE=1`. P50 is 13s and 90% of runs finish
Ship in under 65s; an ETA there is noise on top of a number that is already
effectively zero. Quote a Ship estimate only once `ci-wait` or `merge-wait` is
actually entered — and for `merge-wait` under `AUTO_MERGE=0` quote nothing at all,
because the wait is on a human and is unbounded by construction.

### Verdict — RQ4

**One-sided P80 percentile phrasing, per Step, re-anchored at verify attempt
boundaries, degrading to "taking longer than usual" on overrun and to nothing on
sparse history.**

---

## 5. Architecture / ADR

ADR 0009 §Decision states the invariant: *"Durations are never stored; they are
always derived."* An ETA baseline has to sit on one side of that line.

**Derive at read time.** Zero schema change, and §3 shows accuracy would not suffer:
five prior samples buy all the precision eighty do, and the loop repo holds 139 runs
inside the window. Two problems. First, the window is not a property of the ETA —
it is 7.4 days *today* because 74% of the retention budget goes to event kinds an ETA
never reads (§1), and it shrinks whenever anything else starts emitting more. A repo
running a handful of tickets a week can fall below five samples without any change to
its own behaviour. Second, cost: building a repo-wide baseline at read time means
scanning the full 5000-row pool and re-segmenting every ticket's runs on every poll
of the run view and `/api/v1/instances` — `runStepDurations` today reads one ticket
and is already capped at 5000 events (`internal/webserver/durations.go:37`).

**A never-pruned aggregate.** One narrow row per completed run per Step
— `(repo, ticket, run_seq, step, duration_ms, verify_attempts, ended_at)` — written
once at terminal state. It survives retention, makes the read a single indexed
aggregate, and can be **backfilled** from `phase_logs` for Verify, where §1 measured
exact agreement (median bias +0s) across 244 additional tickets and 36 days.

**Recommendation: the aggregate — but ship the read-time version first.** The
read-time derivation is a genuinely useful thin slice that needs no ADR conversation
(S1 below), and it is what proves the presentation before any schema is added. Add
the aggregate when a second repo needs a baseline the events window cannot hold.

### Sketched ADR 0009 amendment

The invariant worth preserving is **provenance**, not storage: durations must never
be *self-reported* by the pipeline, because a pipeline that reports its own timings
can report them wrongly and nothing contradicts it. A forecast aggregate does not
violate that — it is a materialised result of the same `activity_change` derivation,
written by the hub after the fact, and any row can be recomputed from events while
they last. Suggested wording for a new ADR (next free number is **0022**) amending
0009 rather than editing it:

> Durations remain derived, never authored. The pipeline still reports only
> `activity_change`; it never reports a duration. A **derived** aggregate may be
> materialised by the hub at run terminal for baselines that must outlive
> `EVENT_RETENTION` — it is a cache of the derivation, not a second source of truth.
> Display surfaces that can derive at read time (run detail's per-Step durations)
> continue to do so and are unaffected.

Consequence to record: `runStepDurations` stays the single derivation, and the
aggregate writer must call it rather than reimplement the segmentation, or the two
will drift.

---

## 6. Plumbing sketch (proposal only)

- **Computation** beside the existing derivation in
  `internal/webserver/durations.go` — a `stageForecast(evs, root, ticket)` next to
  `runStepDurations`, reusing `activity.StepOf` so the vocabularies cannot drift
  (ADR 0009).
- **Baseline** from a repo-scoped P80 per Step, plus the attempt-conditional
  remaining-time table for Verify. Cache per repo with a short TTL — the numbers move
  on the timescale of days, and the run view polls.
- **Wire shape**: a sibling of `Durations` on `RunDetail`
  (`internal/webserver/rundetail.go:20`, populated at `:205`), e.g.
  `forecast: [{step, within_ms, basis: "repo"|"global", samples}]`, plus the same
  field on `Instance` (`internal/webserver/instances.go:17`) so live surfaces get it
  without a second request. `within_ms` — not `remaining_ms` — so the client cannot
  turn it into a countdown.
- **Consumers**, all of which already have `state_since` and a ticking `now`:
  - run view header, next to `elapsed / in phase` — `web/src/components/trau/run-view.tsx:897`
  - Loop screen — `web/src/components/trau/loop.tsx:1365` and the epic finalize row `:1422`
  - Overview live card — `web/src/components/trau/overview.tsx:283`
  - Instances page — `web/src/routes/instances.tsx:306`
  - ledger rows — `web/src/components/trau/run-ledger.tsx:166`
  - run detail per-Step table (expected vs actual) — `web/src/routes/runs_.$repo.$ticket.tsx:550`
  - TUI step rows — `internal/tui/steps.go`, which already tracks true Step-entry time
- **Watch out**: the web has no true Step-entry clock (§4) — `state_since` resets on
  every activity *and detail* change, so it must not be used as the Step timer.

---

## Recommendation

| Question | Recommendation |
|---|---|
| **Data source** | `activity_change` deltas as the canonical baseline; `phase_logs` for a one-time Verify backfill only (exact agreement, 2.5× depth). Not `token_calls`/`agent_call` — right coverage, wrong shape. |
| **Model** | Global median per Step, quoted at **P80**; **per-repo for Ship** (repos differ 8% vs 95% MdAPE). No `config_hash` cohort awareness — measured at zero benefit. Attempt-adaptive re-anchoring for Verify, adopted for staleness rather than accuracy. |
| **Presentation** | One-sided **"usually done within Xm"** per Step (77% observed hit rate at P80). Never a countdown, never a range. On overrun (23% of runs) → "taking longer than usual" plus the existing elapsed timer. Sparse history → cross-repo fallback with hedged copy, then nothing. No Ship forecast until `ci-wait`/`merge-wait` is entered; none at all for `merge-wait` under `AUTO_MERGE=0`. |
| **Architecture** | Ship read-time derivation first (no ADR needed). Add a never-pruned per-run aggregate when a second repo outgrows the events window, under a new ADR 0022 amending 0009's storage clause while preserving its provenance rule. |

**Honest error bars to quote in any product conversation:** Build is predictable to
~34% of its own length (MdAE 5m10s), Verify to ~59% (MdAE 6m30s, P90 36m29s), Ship is
a near-constant under `AUTO_MERGE=1`. Verify **cannot** be forecast deterministically:
the attempt count dominates the duration, one extra attempt triples the Step, and the
loop's own structural bound (540m) is 43× the observed median.

---

## Proposed vertical slice breakdown

For a follow-up epic. **Not filed by this ticket.** Each slice is end-to-end and
independently shippable; S1 delivers viewer-visible value with no schema and no ADR.

1. **S1 — read-time P80 forecast on the run view.** Derive a repo-scoped P80 for
   Build and Verify from `activity_change` beside `runStepDurations`; serve
   `forecast` on `RunDetail`; render "usually done within Xm" next to
   `elapsed / in phase` for the *active* Step only. Includes the overrun state
   ("taking longer than usual") and show-nothing on sparse history. No schema, no ADR.
2. **S2 — attempt-adaptive Verify re-anchoring.** Attempt-conditional remaining-time
   baseline; re-quote on `repair`/`bugfix`. Adds a true Step-entry clock to the web
   (`state_since` is unusable for this) mirroring `internal/tui/steps.go:70`.
3. **S3 — never-pruned stage aggregate + phase_logs Verify backfill.** The
   `stage_durations` table written at run terminal via `runStepDurations`; one-time
   backfill; swap S1's baseline onto it. Carries the ADR 0022 amendment.
4. **S4 — cold-start fallback.** Cross-repo baseline with hedged copy and a `basis`
   field on the wire so the UI can say "across repos"; the three-tier sparse-history
   rule.
5. **S5 — widen the surfaces.** Same forecast on ledger rows, the Overview live card,
   the Loop screen and the Instances page, served on `/api/v1/instances` so no
   surface needs a second request.
6. **S6 — TUI step forecast.** Render the quote on the step rows, which already track
   true Step-entry time.
7. **S7 — conditional Ship forecast.** Per-repo Ship baseline, shown only once
   `ci-wait` or `merge-wait` is entered; suppressed entirely for `merge-wait` under
   `AUTO_MERGE=0`.

Sequencing: S1 → S2 → S5/S6 can proceed without S3. S3 is the prerequisite for S4
(cold start is exactly the case the events window cannot serve), and S7 is
independent of all of them.

---

Findings verified against a snapshot of the live hub DB taken **2026-07-26**; the
corpus is 168 completed runs across 7.5 days. Re-run the backtest before acting on
these numbers if the pipeline's stage structure, `EVENT_RETENTION`, or `AUTO_MERGE`
defaults change — all three move the results directly.
