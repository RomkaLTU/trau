# ADR 0036 — An Azure DevOps board reads by its own columns and Stack Rank, and trau writes markdown into the fields Azure keeps it in

- **Status:** Accepted
- **Date:** 2026-08-06
- **Deciders:** Romas (sole maintainer)
- **Ticket:** TRAU-1

## Context

ADR 0033 stopped Azure DevOps grouping from guessing at state names and made it
read the categories the project itself reports. That was the right fix for a
renamed column, and it is still the fallback here. What it cannot fix is a board
with more columns than states: a team running `New`, `Ready to Develop`,
`In Progress`, `Ready to test`, `Done` has columns that share a state, so nothing
derived from `System.State` can tell them apart. `azureUnstartedState` splits
`Proposed` in two and stops there, which leaves the team's own reading of its
board — the Kanban column — invisible to trau.

Order was wrong in the same way. `backlogOrderBy` ranks Todo and Backlog families
newest-created first, which is what a Linear or Jira board wants. An Azure team
orders a column by dragging it, and that drag is `Microsoft.VSTS.Common.StackRank`
— a field trau read nowhere. The board showed one top row and the loop picked
another.

And what trau wrote back did not match the board either. `textToHTML` wrapped every
body in `<div>`s, so a markdown table or a fenced block reached the work item as
escaped prose. Acceptance criteria the reader had lifted out of
`Microsoft.VSTS.Common.AcceptanceCriteria` were written back into
`System.Description` under a heading, so a read-edit-write cycle collapsed the two
fields into one. A create left the item unassigned (ADR 0031 §9) and at whatever
rank Azure DevOps handed out, and only a create carried a tag saying trau had been
there.

## Decision

**A board column is the grouping key; the category path is the fallback.**

- The mapping lives in one web-editable catalog key, `AZURE_BOARD_STATES`
  (ADR 0011), as comma-separated `<board column>=<group>` pairs over
  `backlog | unstarted | started | done | canceled`. It is per-repo configuration,
  not a longer table in Go — which is precisely what ADR 0033 refused, and this
  does not reverse it: with no mapping set, grouping is exactly what ADR 0033
  derives.
- Where a repo sets one the mapping is **authoritative and exhaustive**: a column
  that exists but is not listed groups as `unknown`. Half-answering from the
  categories would hide the gap the operator has to close.
- An item with **no** board column — outside the team's area path, or a Task on the
  sprint taskboard — matches its `System.State` against the same pairs, and failing
  that keeps ADR 0033's category grouping. Off-board work stays pickable instead of
  stranding in Other.
- The mapping drives **board grouping** and **reconcile status** (`IssueStatus`)
  from one resolution, so `--status` reconcile and epic finalization agree with the
  column a human is looking at. It does **not** drive the write path: Azure DevOps
  refuses to write `System.BoardColumn` over the API, so a write still resolves a
  *state* through the project's categories and the `STATUS_*` pins.
- `System.Reason` still overrides a `done` column into `canceled`, the same
  discriminator ADR 0033 applies — Azure DevOps closes a discarded item into the
  same column as a finished one.
- `System.BoardColumnDone` is ignored. The column name alone is the key, and there
  is no seventh `StatusGroup`: "Ready to test" is `started`.
- Because the mapping decides what a row groups as, it joins `scopeKey` beside
  `STATUS_TODO`: two repos mirroring one board share a pull only when they group its
  columns the same way (ADR 0028 §7).

**Board order on an Azure repo is the team's own rank.**

- Within each state-group section, rows sort by Stack Rank ascending, then by
  work-item number descending. The `familyCreated DESC` term is dropped **for Azure
  only** — it would fight the team's ranking — and Linear, Jira and internal boards
  are untouched. State-group sections and ADR 0010's nesting both stay; nesting has
  been a client-side parent-id lookup since ADR 0010's 2026-07-29 amendment, so it
  never depended on the family-key ordering this replaces.
- A row with no Stack Rank sorts last in its section, newest number first. That is
  also where an internal issue filed against an Azure repo lands, which is why the
  order is chosen per repo rather than per row: a per-row switch would split one
  section into two orderings.
- The loop picks in the same order (`azureapi.rank`), so the board's top row is the
  next ticket. Priority stops deciding the pick; the board does.

**Writes land in the fields and the format Azure DevOps expects.**

- Description and acceptance criteria are written as real markdown, each field
  carrying `/multilineFieldsFormat/<field>` in the same patch document. The
  conversion away from HTML is one-way per Microsoft's documentation, which is why
  the format op rides on **every** write of a field trau owns rather than being set
  once and assumed.
- The reader is format-aware for the same two fields: a work item reports each
  multiline field's format alongside its value, and a field stored as markdown is
  read as the markdown it is. Converting it as HTML instead would delete every
  angle-bracket run in it — a generic, an autolink, a fenced JSX block — and the
  next write would store the loss.
- The body splits on the **last** top-level `## Acceptance criteria` heading — the
  one the Azure reader appends the criteria field under — so the split is the exact
  inverse of the emission even for a description carrying that heading itself, and a
  read-edit-write cycle round-trips. A work-item type with no acceptance-criteria
  field (a Task) keeps the whole body, heading included, in the description.
- Comments keep going through `System.History` as HTML. Whether that field accepts
  the markdown format flag is unverified and needs a spike; a real markdown→HTML
  renderer is the fallback. Nothing here changes it.
- Every trau write that changes a work item adds the `trau` tag — description,
  acceptance criteria, labels, state transitions, comments — not only creates.
  `System.Tags` is one flat string, so the mark costs a read of the item's current
  tags before the write.
- A create is assigned to the PAT owner, resolved from `_apis/connectionData`, and
  ranked above the top of the board column it landed in. This amends ADR 0031 §9
  narrowly: identity *search* still needs the `vssps.dev.azure.com` Graph host and a
  scope trau does not request, so `AssignIssue` and `AssignableUsers` stay
  `ErrUnsupported` — but the token's own owner needs neither.
- ADR 0031 §7 is unchanged: a create is still requirement level and a Task is still
  only ever a slice. Everything above applies to trau's edits of a work item at any
  level, Epics and Features included.

## Consequences

- ADR 0024's note that Azure rich-text fields are HTML fragments no longer holds for
  the two fields trau writes; the reader converts HTML for every field whose reported
  format still says so, which is everything trau has never written — comments
  included.
- Schema `0061` adds a nullable `issues.stack_rank` and blanks the sync cursor of
  every azure-sourced repo, so the next tick pulls the whole board once and fills
  every rank — the same self-healing one-shot pull ADR 0033's `0054` performed. NULL,
  not 0, is what sorts an unranked row last.
- A state transition now costs one more read: the tag mark needs the item's current
  tags. A comment and a description write pay the same, and they inherit that read's
  failures — a transient read error now fails a transition or a comment that would
  previously have patched straight through. The mark is on every change or it cannot
  be trusted to separate trau's edits from a teammate's, so the read is not optional;
  a caller retries the whole write.
- A create costs the connection-data read plus a top-of-column lookup. The rank is
  presentation, so a failed lookup is reported and the work item still exists —
  never a create that half-succeeded.
- A repo that maps its columns and then renames one on the board sees that column's
  work under Unknown until the mapping catches up. That is the exhaustiveness
  decision doing its job: an operator-visible gap beats a silently wrong group.
- Glossary gains **Board column** and **Stack Rank**.
