# Durable lessons memory

Trau's pipeline is fixed (build → handoff → verify → commit → PR → CI → merge), and
each phase runs in its own fresh, isolated process — by design, so verify stays cold
and adversarial. The cost of that isolation is that a run learns nothing from earlier
runs: a failure that took two repair attempts to fix leaves only a transcript, and the
next slice that hits the same wall starts from zero.

The **lessons memory** closes that gap without breaking isolation. After a run actually
learns something — verify passed only after a repair, or the slice was quarantined — the
loop distills the experiment into a compact record and records it in the per-repo ledger.
Later runs recall only the records *relevant* to the slice in front of them and fold them
into the build, verify, and repair prompts. Failed runs teach future runs.

## Where the ledger lives

The ledger is a per-repo table in the serve hub's store (ADR 0008). The loop child posts
each distilled lesson to the hub as verify records it and recalls the relevant ones from
there for prompt injection — it never reads or writes a ledger file. The web UI serves the
same records on the **Lessons** page.

A repo that still has a file-era ledger (`runs/memory/lessons.jsonl`, one JSON object per
line) has it folded into the store once, on the hub's first touch of the repo, after which
the file is removed.

## What a record holds

Each line is one **repair experiment**:

```json
{
  "ticket": "COD-123",
  "phase": "verify",
  "failure_type": "migration",
  "attempted_fix": "repair",
  "evidence": ["migration rollback failed on users table", "..."],
  "result": "repaired",
  "lesson": "Run pending migrations before seeding so foreign-key constraints hold.",
  "tags": ["migration", "data"]
}
```

- **`failure_type`** / **`tags`** — a coarse category (and every category that matched)
  derived from the verdict's failure lines. These are the retrieval keys.
- **`attempted_fix`** — `repair`, `bugfix`, `repair+bugfix`, or `none`.
- **`evidence`** — the concrete failure lines from the QA verdict (capped).
- **`result`** — `repaired` (verify eventually passed) or `quarantined` (repairs exhausted).
- **`lesson`** — the distilled, reusable takeaway. This is what gets recalled.

## When records are written (the distillation step)

A record is appended at the two points where there is something to learn:

1. **After a repaired success** — verify passed, but only after one or more repair/bugfix
   attempts. The lesson captures what the fix was.
2. **After a quarantine** — verify exhausted its repair and bugfix attempts. The lesson
   captures what could not be fixed automatically.

A clean first-pass verify writes nothing — there was no failure to learn from.

### Mechanical vs agent-distilled lessons

By default the lesson text is **synthesized in Go** from the verdict — free, deterministic,
no extra agent call. Set `LESSONS_DISTILL=1` to instead run a **cheap, isolated agent pass**
that distills a richer, ticket-agnostic takeaway. The distill agent runs *outside* the
budget-guarded phase path, so a post-hoc distillation can never quarantine a ticket that has
already finished its real work; if it fails or writes nothing usable, the mechanical record
stands. Either way a record is always written when `LESSONS=1`.

## How lessons are recalled

Recall is **relevance-filtered and capped** so the ledger never bloats a prompt:

- **build / verify** prompts recall lessons matching the **ticket's domain** (its title).
- **repair / bugfix** prompts recall lessons matching the **current failure lines**.

Scoring keys off `tags` and `failure_type` (heavy weight) with a lighter signal from
word-overlap on the distilled text, subject to a minimum-score floor. At most a handful of
lessons are injected, each a single line. A thin or empty ledger injects **nothing**, so an
early run sees no change in behavior.

Recalling cross-run lessons into the cold verify prompt does **not** break verify's isolation:
lessons are durable, distilled guidance from *other* runs, never this run's build reasoning.

## Configuration

Both knobs live in `trau.ini` (see `trau.ini.example`):

| Knob | Default | Meaning |
|------|---------|---------|
| `LESSONS` | `1` | Master switch: record + recall. `0` disables the whole feature. |
| `LESSONS_DISTILL` | `0` | `1` = enrich each lesson via a cheap agent pass; `0` = mechanical only. |

## Safety & failure modes

Recording and recall are **best-effort and never block the loop**:

- A malformed line in a folded-in legacy ledger is skipped; a hub with no records reads as
  an empty ledger.
- A write failure is silent — the ledger is an optimization, not a checkpoint.
- The feature adds no agent calls unless `LESSONS_DISTILL=1`, and even then only on
  failure-path tickets (a repaired success or a quarantine).

## Sharing lessons across contributors

The ledger lives in the hub store, private to the machine running the loop — it is not a
tracked file, so per-developer runs never fight over it. That is the right default while the
loop and provider behavior churn: a lesson distilled today may be noise next week.

To make a lesson **durable, repo-wide, and reviewed**, promote a *curated digest* of those
lessons into a **tracked** location — e.g. a committed `.trau/memory.md` — where it ships with
the repo and applies to every contributor. Curate first: the raw ledger is an unfiltered,
machine-appended stream that can contain run-specific noise.
