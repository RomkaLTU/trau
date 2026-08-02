# ADR 0033 — Azure DevOps states group by the categories the project reports, and one resolution decides the todo column

- **Status:** Accepted
- **Date:** 2026-08-02
- **Deciders:** Romas (sole maintainer)
- **Ticket:** TRAU-30

## Context

A team running a customized Azure DevOps process — `New`, `Ready to Develop`,
`In Progress`, `Done` — saw its board group wrongly in three separate ways.

`azureapi.Category` classified a state by switching on its *name*, covering only
the vocabulary the stock Agile, Scrum, CMMI and Basic templates ship with. "Ready
to Develop" is in none of them, so it fell to `CategoryUnknown`: the board
rendered it under "Other", and `IssueStatus` reported `StatusUnknown` — which
makes reconcile and epic finalization deliberately skip the ticket. The
categories Azure DevOps itself publishes were already being read for the write
path and thrown away everywhere else.

`mapAzureGroup` had no branch that produced `backlog` at all, so raw intake work
sat in the board's Todo section next to groomed, ready-to-pick work. And the
shared `Stage.names()` vocabulary lists "New" ahead of "Ready", so a loop reset
parked a ticket back in the intake column rather than the one the team picks
from.

The constraint that shapes the fix: **both** "New" and "Ready to Develop" report
category `Proposed`, so the category alone cannot tell the two columns apart.

## Decision

**A state's category comes from the project's own process, and the one Proposed
state trau would write for the todo stage is the one that reads back as
unstarted.**

- **The category is what Azure DevOps reports, not what a table guesses.**
  `Client.StateCategories` memoises the work-item type's states per project and
  type, and `CategoryOf` classifies against them. The name table stays as the
  fallback for a state the response omits and for a metadata read the token
  cannot make — degrading to the old behaviour, never failing the sync. The
  table is exactly what fails on a customized process and would fail on the
  next one, so it stops being the primary answer instead of growing longer.
- **One resolution serves both directions.** `azureUnstartedState` picks the
  ready-to-pick column once: the write path targets it for `StageTodo`, and the
  read path groups it as `unstarted` and every other `Proposed` state as
  `backlog`. Read grouping and write target cannot drift apart. A process with a
  single `Proposed` state resolves that one, so stock Agile and Basic group
  exactly as before; Scrum splits `New` from `Approved`.
- **`STATUS_TODO` remains the escape hatch and now decides both.** The pin wins
  outright over the vocabulary, in the write and in the grouping alike, which is
  why the hub's own reader carries it (`readerConfig`) rather than only the
  loop's tracker. A pin naming a state outside `Proposed` leaves every `Proposed`
  state grouped as `unstarted` rather than hiding a whole column under Backlog.
  No new config keys.
- **The ready-over-new preference is Azure-only.** `azureTodoNames` lives beside
  the Azure provider; the shared `Stage.names()` vocabulary is untouched, so no
  Jira or Linear repo changes behaviour.
- **Mirrored rows re-group without user action.** Azure syncs are incremental, so
  schema migration `0054` blanks `issue_sync.cursor` for the repos whose issues
  were filed under the azure source. The next tick pulls the whole board once and
  the one after resumes incrementally — the same self-healing one-shot full pull
  `pullCursor` performs.

`WorkItem.Done()` and the blocker/epic-child completeness checks keep classifying
by the name table; a customized *done*-named state is a separate problem.

## Consequences

- A customized process groups correctly with no configuration, and no Azure row
  lands in `unknown` merely because someone renamed a column.
- A board read costs one extra metadata request per work-item type per sync,
  memoised on the client — including the failure, so a token that cannot read
  process metadata does not pay a round trip per item to learn that repeatedly.
- Ticket picking is unaffected: it runs off the WIQL query plus the ready label,
  so regrouping intake work as `backlog` does not stop the loop picking anything.
- ADR 0026 §3's note that the read-side `azureapi.Category` mapping is unchanged
  no longer holds; the name table is now the fallback rather than the rule.
