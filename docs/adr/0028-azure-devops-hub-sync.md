# ADR 0028 — Azure DevOps hub sync

- **Status:** Accepted
- **Date:** 2026-07-31
- **Deciders:** Romas (sole maintainer)

## Context

ADR 0024 §4 deliberately left Azure DevOps out of the hub's sync: `NewReader`
refused `azure` the way it refuses GitHub, so an Azure repo could only be driven by
the loop reading the tracker live. Everything the hub builds on its own mirror was
dark for those repos — the backlog board, the Inbox, internal search, and the
queue-by-id confirm path, which all read the store rather than the tracker.

Nothing about the read model blocked it. Two facts settle the shape:

- A flat WIQL query (`POST {org}/{project}/_apis/wit/wiql`) returns ids only, and
  `WHERE [System.ChangedDate] >= '<ts>'` makes the pull incremental. Details come
  from a batched `GET /_apis/wit/workitems?ids=…&$expand=relations`.
- `GET {org}/_apis/connectionData` authenticates with the PAT and answers with
  `authenticatedUser`, which is enough for `Reader.Identity`.

Both ride the PAT scopes ADR 0024 §3 already requires. Nothing new has to be
minted.

## Decision

Add an `azureReader` behind `tracker.NewReader`, so the hub syncs Azure DevOps
through the same seam as Linear and Jira and no caller learns a new provider.

1. **Ids from WIQL, details from a batch read.** `SyncIDs` renders one flat query —
   team project, optional area, optional changed-since — and hands its ids to the
   existing batched `WorkItems` read. `ProjectIdentifiers` is the same query with no
   cursor, which is what makes the reconcile sweep (ADR 0007) cheap.
2. **The cursor is compared at second precision.** `System.ChangedDate` comes back
   at whatever sub-second precision the service chose, which the WIQL date clause
   will not parse. The stored cursor is truncated to whole seconds and compared
   inclusively, so the window can only widen: the boundary item is re-pulled — an
   idempotent upsert — rather than missed. A cursor that will not parse falls back
   to a full pull.
3. **An Area Path filter scopes the whole provider, not just the mirror.** The new
   `AZURE_AREA_PATH` key narrows the sync with `[System.AreaPath] UNDER`, and the
   loop's own eligible query carries the same clause. A board the hub mirrors but a
   loop picks outside of would disagree with itself at the queue-by-id confirm.
   Empty is the whole team project, which is every existing repo's behaviour.
4. **A discussion is read only when the work item reports one.** Azure DevOps
   serves comments one work item at a time, so a bulk pull would otherwise spend a
   round-trip per ticket. `System.CommentCount` gates the call, and the comment's
   own id rides along as the stored `ExternalID` so a re-pull updates the entry
   instead of filing it again. A failed discussion read costs the pull its comments,
   never the ticket.
5. **`Reader.Issue` reads organization-scoped.** The work-item route addressed
   without a project resolves an id whatever team project owns it, so a ticket from
   another project comes back with `InProject` false — the refusal the hub can
   explain — rather than the 404 a project-scoped read would answer with.
6. **Writes are unchanged.** `NewWriter` still refuses `azure` with
   `ErrUnsupported`. The loop's own writes (state, tags, comments, bug filing) keep
   going through `AzureDevOps`; only the hub's read path is new.

## Consequences

- A repo with `TRACKER_PROVIDER=azure`, `LINEAR_TEAM`, `AZURE_ORG_URL` and
  `AZURE_PAT` now gets a synced board, Inbox, search and queue-by-id confirm, with
  no new configuration and no new PAT scope.
- Work-item file attachments are **not** mirrored. Their bytes sit behind the same
  PAT the pull holds, which the hub's attachment surface cannot present, so
  `SyncedIssue.Attachments` stays empty for `azure`. The description and comments
  still carry whatever the tracker rendered inline.
- A full pull is capped at the 20000 rows a flat WIQL query will serve. A team
  project past that ceiling needs an Area Path to divide it.
- Narrowing `AZURE_AREA_PATH` on a repo that already synced tombstones the work
  items now outside the area on the next reconcile — correct, and reversible by
  widening the key and resyncing.
- Filing an Azure DevOps work item from the web UI is still unavailable, and the
  backlog-unavailable hint no longer names Azure DevOps as unmirrorable — GitHub is
  now the only provider the hub cannot sync.
