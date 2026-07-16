# ADR 0010 — Backlog board epic hierarchy: status-true nesting, collapsed by default

- **Status:** Accepted
- **Date:** 2026-07-14
- **Deciders:** Romas (sole maintainer)

## Context

Every backlog row has carried its hierarchy since the add-all epic (COD-806):
`BacklogEntry.parent` names the epic a sub-issue belongs to and `has_children`
marks an epic. The board ignores both — rows render flat inside the
planned-first status sections that just shipped (COD-851 epic), so a repo
whose backlog is mostly sliced epics reads as an undifferentiated list. The
rest of the product already treats an Epic as a unit: queueing one queues its
sub-issues, drain settles them together, and the Loop timeline expands them.
The board is the one surface where the structure is invisible.

The obstacle is that an epic and its sub-issues rarely share a status — the
epic sits in Backlog while one slice is In Progress and another is Todo. Full
tree nesting ("children always under their epic") and truthful status
sections ("In Progress lists what is in progress") cannot both hold. Which
gives, and everywhere else this decision leaks — pagination, filters,
collapse state, progress counts — is what this ADR fixes.

## Decision

**Status-true nesting.** Sections keep telling the truth. A sub-issue nests
under its epic only when both are visible in the same status section — the
common case, since slices start life in Backlog together. A sub-issue whose
status has diverged stays flat in its own section with a breadcrumb chip
naming its epic (`↳ COD-851`); the chip opens the epic in the `?issue=`
drawer, which works even when the epic is paged out or in a hidden
Done/Canceled section. Nesting is presentation over whatever rows the current
page and filters produced — it never invents rows.

**Collapsed by default, remembered locally.** An epic renders as a single
row with a chevron; the Backlog section reads as a list of planned units.
The chevron toggles children; clicking the row itself opens the drawer, as
on every other row. The set of expanded epic ids persists per repo in
localStorage — never in the URL, which stays reserved for semantic state
(filters, `?issue=`). Collapsing hides rows client-side only; totals and the
pager are unaffected.

**Progress is settled/total.** A collapsed epic shows `◑ settled/total`,
where settled = done + canceled — nothing left to run, the same semantic the
queue drain applies when it settles an epic. Remaining work is always
`total − settled`. The counts cover *all* of the epic's children in the
store, not the filtered page, so the hub computes them: `BacklogEntry` gains
`children_settled` / `children_total`, present only when `has_children`.

**Filters are strictly per-row.** Search and filters match individual rows
exactly as today. An epic filtered out while its children match → children
render flat with chips. An epic that matches while its children don't → the
epic shows alone (its progress numbers still cover all children). No ghost
context rows, no family cascade — a `ready-for-agent` filter must not drag
in non-ready siblings.

**Adjacency via ordering, flat contract.** `backlogOrderBy` gains a family
key: within a status group, rows sort by the numeric-aware identifier of
`COALESCE(parent, identifier)`, epics before their children, then own
identifier. The response shape, pagination, and section counts are
untouched; the client nests adjacent runs where the parent row is visible.
Children whose epic sits in another group cluster by epic id within their
own section — an accepted, mildly useful side effect (siblings group
together). Children that start a page without their parent row render flat
with chips.

**One level.** Hierarchy on the board is epic → sub-issue, matching the one
level the queue's `sub_issues` and add-all support. A deeper tree renders by
immediate-parent chips only; family-key ordering makes no adjacency promise
past one level.

## Consequences

- Hub: the `backlogOrderBy` family key and the two child-count columns on
  the backlog query (a grouped self-join or subquery over `parent`); the
  flat `BacklogResponse` contract and its continuation-header logic survive
  untouched.
- Web: collapse/expand render keyed off `has_children`, breadcrumb chips off
  `parent`, expanded-set in localStorage per repo. Sections, pagination, URL
  filters, and the drawer are reused as-is.
- Section headers count every matching row, including children hidden by a
  collapsed epic, and a fully-collapsed page can render fewer visible rows
  than the page size. Both are honest and accepted; the chevron and progress
  make the hidden rows discoverable.
- An epic with a canceled child does reach n/n. Reading `6/6` as "six
  completed" is the small lie accepted in exchange for progress that reaches
  its end exactly when the epic has nothing left to run.
- Glossary gains **Epic**, **Sub-issue**, and **Settled** — the board, the
  queue, and drain now share one vocabulary for them.

## Amendment (2026-07-17)

An epic's board group is now derived: a not-yet-closed epic files under the
started group while any of its live children is started, so in-flight work
lists the whole family as in progress rather than just the sub-issue taken
from it. The derivation drives the ordering, the section counts, the state
filter, and the row's reported group together, and the epic's started
children nest under it in the In Progress section. The epic's stored status
is untouched; children whose status has diverged keep the flat-with-chip
rendering, and a done or canceled epic is never reopened by a started child.
