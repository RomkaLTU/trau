# ADR 0026 — App URLs move to the hub store, and the hub wins wholesale

- **Status:** Accepted
- **Date:** 2026-07-30
- **Deciders:** Romas (sole maintainer)
- **Refs:** [COD-1335]; [ADR 0008](0008-db-first-run-data.md) (the hub owns run data); [ADR 0019](0019-per-workspace-config-scoping.md) (workspace resolution from the slice's diff)

## Context

Browser verify's target came from two config keys: `APP_URL` for the repo's app and
`APP_URLS` for a monorepo's per-workspace apps, a comma-separated
`<workspace>=<url>` string. The pipeline resolves them per slice — the workspace
holding the changed files wins, everything else falls open to `APP_URL`.

That shape is at its limit. A URL is not a preference the way `BROWSER_VERIFY=auto`
is; it is a record about a repo, with a lifecycle (added, relabelled, retired) and
things that want to hang off it — QA accounts that only exist on one of the apps
being the immediate one. `APP_URLS` had already started encoding a list inside a
single string value, which is what a table looks like before it has a table.

The hub already owns exactly this kind of per-repo record: QA credentials
(`hubstore.QAAccounts`), prompt overrides, lessons. App URLs belong with them.

## Decision

### 1. `app_urls` is a per-repo table, keyed on the workspace

One row per browser target: `url` (the only required field), an optional `label`
for the humans reading it, and a `workspace`. Uniqueness on `(repo, workspace)`
does double duty:

- each workspace holds at most one entry — the `APP_URLS` role;
- the entry with an **empty** workspace is the repo's default target — the
  `APP_URL` role — and there is at most one of it, because the empty string is
  just another workspace value as far as the constraint is concerned.

No separate "is_default" flag, no partial index, no application-level guard that
could disagree with the schema. `hubstore.AppURLs` exposes the usual
Create/List/Get/Update/Delete plus `ByWorkspace`, which the API calls first so a
clash comes back as a 409 naming what is taken rather than a raw constraint error.
That pre-check and the write are not atomic, so the constraint — not the check —
is what actually holds the invariant: the store translates its violation into
`ErrAppURLWorkspaceTaken`, which the API answers with the same 409, and the
driver's text never reaches the client. `List` orders by workspace, which puts the
default row first.

### 2. The hub wins wholesale, per run

The pipeline resolves its targets once at ticket-run start (`loadAppURLs`, beside
`loadPrompts`). If the hub returns **any** entry, the stored set becomes the run's
whole answer: the workspaceless entry populates `AppURL`, the workspace entries
populate `AppURLs`, and the config values are not consulted. If the hub returns
none — or cannot be reached, which logs one warning and never blocks a run — the
config values stand exactly as before.

Wholesale, not per-field. The alternative — hub default plus ini workspaces, or
either direction of key-level merge — makes the effective target set a function of
two half-populated surfaces, and the question "why is verify driving *that*" then
has no local answer. Wholesale means the answer is always "look at one place." The
visible consequence is deliberate: a repo whose hub entries are all
workspace-scoped has **no** default target even if `APP_URL` is set, so an
unmatched slice gets the advisory gate. That is the same signal an unset `APP_URL`
has always given, and it is what makes the precedence readable.

The configured values are snapshotted before the first resolution, because one
pipeline serves every ticket in a session: without the snapshot, a run that
followed a run with hub entries would resolve against the previous run's result
rather than the ini values.

### 3. `sliceAppURL` is untouched

Per-slice routing — the workspace whose directory holds the changed files wins,
via `agent.WorkspaceAppURL`, failing open to the default — reads `AppURL` and
`AppURLs` and neither knows nor cares which surface filled them. Keeping the
resolution at load time rather than at the call site is what makes "byte-for-byte
today's behavior with no entries" true by construction instead of by review.

The run keeps only the resolved URLs, not the entry ids they came from. A later
per-entry lookup — QA accounts scoped to one app — needs that mapping, but it can
carry it in the shape that slice actually wants; a URL → id map added ahead of a
reader would collide whenever two workspaces point at one URL.

### 4. Doctor reports both surfaces

`checkBrowserVerify` passes when the hub holds an entry for the repo, reading the
hub database read-only as the team-sync check does, so the answer is the same
whether or not the hub is up. An unreadable database falls back to the config keys
rather than reporting a missing target. The warning names both surfaces, since with
neither populated either one is a valid fix.

## Consequences

- The ini keys become a read-only fallback. Nothing writes them from the hub and
  nothing migrates them into it: a repo that never adds an entry keeps working
  unchanged, and one that adds an entry has said which surface it means.
- A repo can now be given a browser target without editing a file, and the entries
  are addressable — which is the point, since the follow-up work scopes QA accounts
  to them.
- Two places can name a target, so "what does verify drive" has one more question
  in front of it. Wholesale precedence and the doctor line are what keep that
  answerable; a merge rule would not have been.
- `APP_URLS` keeps its comma-separated encoding for as long as the fallback exists.
  It is nobody's favourite format, and re-cutting it now would break the one
  promise this ADR makes about repos that do not opt in.

[COD-1335]: https://linear.app/codesomelabs/issue/COD-1335/hub-stored-app-url-entries-drive-browser-verify
