# ADR 0026 — Workflow-aware status transitions

- **Status:** Accepted
- **Date:** 2026-07-31
- **Deciders:** Romas (sole maintainer)

## Context

The loop moved tickets by naming a status literally: `SetStatus(ctx, id, "In
Review", …)`. Those names are Trau's own vocabulary, and workflow statuses belong
to the project. A Jira workflow whose review status is `READY FOR QA` has no
status called "In Review" to transition to, so every ticket the loop processed
logged

```
status (In Review) error: jira: no matching transition to "In Review" (available: READY FOR QA, To Do, In Progress, Done)
```

The write is non-fatal, so the run shipped — but the ticket never reflected review
state, and the same failure mode existed for every other stage. A 16-scenario
calibration campaign against a Jira-backed consumer repo hit it on every run.

Each provider had grown its own partial answer: Azure DevOps resolved a target
name against the process template's states by category (ADR 0024), the internal
provider substring-matched a display status onto a state group, Linear matched a
state name exactly and failed otherwise. The vocabulary was duplicated four times
and only correct in one of them.

## Decision

**The pipeline names a lifecycle stage; the provider resolves it against the
workflow its tracker reports.**

1. `tracker.Stage` (`todo`, `in-progress`, `in-review`, `done`) replaces the
   status string in the `Tracker.SetStatus` signature. It is the only status
   vocabulary in the codebase, and it never travels to a tracker unresolved.
2. `tracker.ResolveStage` picks a destination among the `WorkflowOption`s a
   provider fetched, in one fixed order: a status name pinned in config, then the
   names the stage commonly goes by, then the first sensible option in the
   provider's category for that stage. Statuses that read as an abandonment
   ("Won't Do", "Cancelled") never stand in for a stage, so a delivered ticket is
   left open rather than closed as unwanted.
3. The `*api` packages stay transport: `jiraapi` exposes `Transitions` and
   `ApplyTransition` instead of resolving a name itself, `linearapi` exposes
   `WorkflowStates`, and `azureapi.SetState` writes a state the caller already
   resolved. This supersedes the resolution half of ADR 0024 §2; the read-side
   `azureapi.Category` mapping is unchanged. (ADR 0033 later demoted that mapping
   to a fallback behind the categories Azure DevOps reports for itself.)
4. `STATUS_TODO` / `STATUS_IN_PROGRESS` / `STATUS_IN_REVIEW` / `STATUS_DONE` pin a
   stage to an exact status name for the workflows where a category holds two
   plausible candidates. They take the standard `TRAU_*` env aliases and layer
   like every other key (ADR 0016). An override the workflow does not offer falls
   through to normal resolution rather than stranding the stage.
5. `DELIVERED_STATE` names where a merge parks a ticket, overriding `STATUS_DONE`
   for that one write. A workflow whose QA gate makes the delivered state
   non-terminal keeps its epic gate on the loop's own merge record, and the
   epic-finalize self-heal restores only a child that fell *behind* the delivered
   state — a human moving delivered work forward is never undone. Only a delivered
   state the stage vocabulary reads as terminal keeps the older rule that any live
   status is a regression; a name it cannot place is treated as the live column it
   almost certainly is, so an unrecognised state never means Done by accident.

## Consequences

- A workflow that renamed every status still lands each stage correctly, with no
  configuration. Overrides exist for the ambiguous cases, not the common one.
- Jira and Linear each cost one extra read per status change (the transition list
  / the team's states). Status changes happen a handful of times per run.
- A stage that resolves to nothing is a real error, surfaced rather than retried
  through the MCP: an MCP call has no destination to transition to either.
- Adding a provider means mapping its category keys onto the four stages, not
  re-deriving what "in review" means.
