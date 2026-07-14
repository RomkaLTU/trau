# ADR 0012 — Web onboarding wizard: the hub can bootstrap a new repo

- **Status:** Accepted
- **Date:** 2026-07-15
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-907 (implements COD-850)

## Context

ADR 0003 gave the web hub repo *registration* but not *bootstrapping*: a repo
had to arrive already configured (a hand-written `.trau.ini` with a tracker
provider and credentials) before the web was useful. First-run setup — pick a
tracker, enter credentials, choose the ready label, seed the backlog — lived
only in the TUI onboarding flow. `docs/cli-web-parity.md` recorded this as a
deliberate gap: "the web edits an already-configured, allowlisted repo through
Settings; it never bootstraps a new one."

The melga incident (a half-configured repo whose `TRACKER_PROVIDER` was unset
while Jira credentials were present, so sync silently guessed Linear and failed)
showed the cost of that gap: the failure mode a first-run wizard is meant to
catch had no web surface at all. COD-850's maintainer direction reversed the
decision — the web should be able to take a bare repo path to a live backlog.

The backend for this shipped first as COD-902 (findRepo resolves a
registered-but-never-run repo, so post-registration config writes and CTAs
resolve), COD-904 (`POST /repos/inspect`, registration returns its seed outcome,
gitignore-ensure), and COD-906 (tracker connection test with team/project
discovery). This ADR records the front-end decision COD-907 builds on them.

## Decision

### 1. The web bootstraps a repo through a staged wizard

`/projects/new` hosts a `path → detect → tracker → essentials → seed-sync → done`
wizard, reachable from the sidebar CONFIGURE group and the repo switcher. Each
step is wired to the real hub, keyed by the inspected repo path:

- **path** → `POST /repos/inspect`, then `POST /repos` with `sync: false` so the
  wizard drives its own seed sync once tracker config is written. Registering
  here (rather than at the end) is what lets every later repo-scoped config write
  and CTA resolve — the findRepo fix (COD-902) is the enabling change.
- **detect** renders the inspect findings, including the provider-vs-credentials
  mismatch warning (the melga trap) — never hidden.
- **tracker** writes `TRACKER_PROVIDER` explicitly, tests the connection, and
  gates Continue on a passing test; the team/project picker is fed from the test
  response. Secrets are write-only (see §2).
- **essentials** writes base branch / ready label / epic flow through the ADR
  0011 per-key config path (project layer) and runs the gitignore-ensure action.
  Everything is defaulted, so the step never blocks.
- **sync** runs `POST /repos/{repo}/sync`; a failed seed sync blocks *done* and
  routes back to the tracker — it is never silent.

### 2. Secrets stay write-only end to end

The wizard never reads a credential back. The inspect response reports only that
a credential exists and in which layer; the tracker step submits a secret only
when its field is non-empty, so leaving it blank keeps the stored value. This
matches the ADR 0011 settings-surface rule and keeps re-running onboarding over a
configured repo safe.

### 3. Registration stays fail-closed

The wizard changes nothing about the exposure boundary from ADR 0003. Inspect,
register, gitignore, and test-connection all pass through the same
`SERVE_ALLOW_REGISTER` gate; on an exposed bind without it the hub answers 403,
which the path step renders as a designed remediation callout rather than a raw
error string.

## Consequences

- **Positive:** a bare repo path reaches a live backlog from the browser; the
  melga failure mode now surfaces during setup; the parity doc's onboarding gap
  is closed and reversed into a mapped surface.
- **Negative / follow-ups:**
  - `BASE_BRANCH` became web-editable so the essentials step can persist it; it
    is now editable from Settings too, which is consistent but widens that
    surface by one key.
  - Seed sync is synchronous (one request); the wizard shows a pending state
    rather than streamed progress. If seeding a large tracker ever feels slow,
    streaming or health-polling is a follow-up, not a blocker.
