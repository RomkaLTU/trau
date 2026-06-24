# Cross-vendor verify panel (the moat)

By default Trau verifies a slice with a single fresh, cold, adversarial process.
The **verify panel** runs the verify phase as *several* fresh processes from
**different providers** — e.g. Codex verifies a Claude build, Kimi breaks ties —
each independently judging the diff against the same handoff brief and the same
[check library](verify-checks.md). Their verdicts are merged by a configurable
policy; a disagreement blocks the merge and triggers the existing cold repair
path, after which the panel re-runs.

This is the logical endpoint of two things Trau already does — a fresh
process-per-phase verify, and provider-agnostic routing. Multi-vendor
orchestration exists elsewhere; cold *adversarial cross-vendor verify* does not.

## Configure

Two knobs in `trau.ini` (off by default — no panel means the single-verifier
behavior is unchanged):

```ini
# Each member is a provider:model:effort spec, same grammar as a phase route.
# model and effort are optional and fall back to that provider's configured
# defaults. Repeated providers are allowed (they get a numeric suffix).
VERIFY_PANEL=claude,codex:gpt-5.5,kimi

# How the members' verdicts combine.
VERIFY_PANEL_POLICY=unanimous       # unanimous | majority | any-pass
```

Every provider named must be a known provider (`claude`, `codex`, `kimi`, or a
custom registered one) with its CLI on `PATH` — otherwise the loop refuses to
start with a clear error, rather than silently dropping a verifier.

## Merge policies

The panel passes (and the slice may proceed to commit/PR/merge) according to
`VERIFY_PANEL_POLICY`:

| Policy        | Passes when…                          | Use for                                   |
|---------------|---------------------------------------|-------------------------------------------|
| `unanimous`   | **every** verifier passes (default)   | Maximum safety: any single dissent blocks. |
| `majority`    | a strict majority pass                | Tolerate one flaky/over-strict verifier.   |
| `any-pass`    | at least one verifier passes          | Lenient; rarely what you want.             |

`unanimous` is the default and the most conservative: it is exactly "any verifier
failing blocks the merge", which is the headline behavior — a defect only one
vendor catches still stops the slice.

## How it works

1. **Fan-out.** For each panel member Trau builds a backend (its own provider,
   model, effort) and runs it as a fresh, isolated process — a new session, no
   continuation. Each verifier inherits only the handoff brief and the code (plus
   checks) on disk; it never sees the build agent's reasoning or the other
   verifiers' verdicts. Members run sequentially and each writes its own verdict
   to `/tmp/verify-<id>-<member>.json`.
2. **Per-member gating.** Each member's verdict is gated by the
   [check library](verify-checks.md) just like a solo verify (failing
   `error`-severity checks fold into that member's failures).
3. **Merge.** The gated member verdicts are merged by policy into one verdict.
   When it does not pass, every dissenting member's failures are carried over,
   tagged with the member name, and the merged verdict is written to
   `/tmp/verify-<id>.json`.
4. **Repair & re-run.** A blocking merged verdict routes to the same
   self-heal/repair path as a solo verify (the primary provider repairs), and
   then the **whole panel re-runs** — up to the configured repair/bugfix budget.
5. **Stop conditions.** A provider rate/usage limit or a budget give-up from any
   member is propagated (the ticket stays resumable on its branch / is
   quarantined) instead of being miscounted as a dissenting fail.

## Ledger & events

Each verifier is a normal agent call, so its tokens and estimated cost land in
the per-ticket ledger (`runs/<id>/tokens.jsonl`) and its `agent_call` event in
`runs/events.jsonl`, under a distinct `verify-<member>` phase label (and
`verify-retry<n>-<member>` on a re-run). `trau --status` therefore attributes
panel cost per verifier.

## Default (single verifier)

With `VERIFY_PANEL` empty, verify runs exactly one process as before — the panel
adds no cost or behavior change unless you opt in.
