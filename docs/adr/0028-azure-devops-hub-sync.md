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

   **Amended: truncation was never the whole story.** The WIQL endpoint runs at
   *day* precision unless the request asks otherwise, and refuses any literal
   carrying a clock — so every incremental pull answered
   `HTTP 400: You cannot supply a time with the date when running a query using date
   precision`, the cursor never advanced, and the board sat behind a stale-data
   warning indefinitely. `timePrecision=true` is a documented query parameter on
   Query By Wiql and every query now sends it. Second-precision truncation stays,
   because seconds is the finest a WIQL date clause parses even with the flag on, and
   the inclusive comparison still absorbs the cursor being the lexical max of stamps
   with variable sub-second precision.
3. **A board scope filter scopes the whole provider, not just the mirror.** The
   `AZURE_AREA_PATH` key narrows the sync with `[System.AreaPath] UNDER`, and the
   loop's own eligible query carries the same clause. A board the hub mirrors but a
   loop picks outside of would disagree with itself at the queue-by-id confirm.
   Empty is the whole team project, which is every existing repo's behaviour.

   **Extended: scoping by naming teams.** An Area Path is not how a team's board is
   actually defined — a team owns a *set* of area values, each optionally carrying
   its children — so an area filter could not express "this repo mirrors the Platform
   team's board". The new `AZURE_TEAMS` key holds a comma-separated list of team
   **names**; trau reads each team's own board settings and inlines the values it
   finds into the WIQL. Several teams mirror the union of their boards, and
   `AZURE_TEAMS` combines with `AZURE_AREA_PATH` — both narrow, so a query carrying
   each asks for their intersection.

   Two facts fix the shape. `teamsettings/teamfieldvalues` accepts a team **name** as
   well as its id, so no GUID lookup is needed first. And `@TeamAreas` is a
   web-portal-only macro that answers *empty* over REST — a query written with it
   silently returns nothing — so the values are read explicitly. A named team that
   declares no area is refused rather than mirrored, since a scope that resolves to
   nothing would silently widen the query back to the whole team project.

   The by-id read carries the scope too. Team-project membership is what a work item's
   own payload can answer, and it is not the question: an item under another team's
   area is in the project and off the board. So a scoped repo asks the board itself
   whether it covers the id, and queue-by-id refuses one it does not — the same answer
   the loop's own pick would give.

7. **One pull per Azure scope, fanned out to each repo.** Every repo bound to a team
   project used to mirror it whole: the same work items appeared twice, and both
   repos spent the same PAT's request budget fetching them. Now repos that share an
   organization, team project and team scope produce a byte-identical read, so the
   first one in flight runs the WIQL, the batch read and the comment sweep and the
   rest of that tick read its answer — each still storing its own rows. Coalescing
   releases every sharer at the same instant, so those stores land concurrently;
   the hub database begins its transactions `IMMEDIATE` for exactly that reason
   (ADR 0007 §1).

   The sharing dedupes the *work*, not the storage: re-keying `issues`,
   `issue_comments`, `issue_sync`, `issue_tombstones`, `issue_relations`,
   `attachments` and the FTS index off something other than `repo` is a much larger
   change for a benefit the shared pull already delivers.

8. **A failed pull keeps the last good mirror, and names the stage that failed.**
   There is no partial-mirror mode: a pull either lands whole or the previous mirror
   stands behind a warning. What the warning was missing is *where* it broke, so each
   stage of an Azure pull now names itself in its error — resolving the board scope,
   querying the board, reading the work items, reading the blockers — and a banner
   reads as a diagnosis rather than only as "stale".

   The discussion sweep is the exception it always was: losing a discussion costs the
   pull that discussion, never the ticket, so a failed comment read is counted and
   logged. It is also the one stage that fans out — Azure DevOps serves comments one
   work item at a time, and serially a first full pull outlasts any sync budget.
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

   **Reversed by [ADR 0031](0031-azure-devops-work-item-hierarchy.md) (TRAU-24).**
   `NewWriter` now builds an `azureWriter` from the same org URL and PAT the reader
   holds, so every grill disposition works on an Azure repo. Assignment is the one
   surface still on `ErrUnsupported`, for the reason ADR 0031 records.

## Consequences

- A repo with `TRACKER_PROVIDER=azure`, `LINEAR_TEAM`, `AZURE_ORG_URL` and
  `AZURE_PAT` now gets a synced board, Inbox, search and queue-by-id confirm, with
  no new configuration and no new PAT scope.
- Work-item file attachments are **not** mirrored. Their bytes sit behind the same
  PAT the pull holds, which the hub's attachment surface cannot present, so
  `SyncedIssue.Attachments` stays empty for `azure`. The description and comments
  still carry whatever the tracker rendered inline.
- A full pull is capped at the 20000 rows a flat WIQL query will serve. A team
  project past that ceiling needs an Area Path or a team to divide it.
- Narrowing `AZURE_AREA_PATH` or `AZURE_TEAMS` on a repo that already synced
  tombstones the work items now outside the scope on the next reconcile — correct,
  and reversible by widening the key and resyncing.
- A first full pull is a genuinely long request: one board-wide WIQL the service
  spends real time planning, then the batched detail reads and the comment sweep. The
  per-request ceiling and the per-repo sync budget are both set for that pull rather
  than for the incremental one, so the widest case completes instead of timing out
  short of its first row.
- No work-item-type filter. The board renders parent chips from `System.Parent`, so
  dropping Epics and Features would leave stories pointing at items that were never
  synced; scoping already collapses the row count that filter was meant to.
- Filing an Azure DevOps work item from the web UI is still unavailable, and the
  backlog-unavailable hint no longer names Azure DevOps as unmirrorable — GitHub is
  now the only provider the hub cannot sync.
