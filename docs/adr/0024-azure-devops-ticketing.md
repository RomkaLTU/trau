# ADR 0024 — Azure DevOps ticketing

- **Status:** Accepted
- **Date:** 2026-07-28
- **Deciders:** Romas (sole maintainer)

## Context

Trau supported four ticket backends: Linear, Jira, GitHub, and the hub's own
internal issues. Azure DevOps Boards was the remaining gap for teams whose
repositories already live in Azure Repos or whose work items are tracked there.

Azure DevOps differs from every provider already wired in ways that decide the
shape of the integration:

- **Identifiers.** Work items are numbered organization-wide and carry no
  per-project key. Every other part of trau — branch inference, `ValidatePrefix`,
  the `PICK=`/`BUG=` sentinel parsers, the web UI — addresses tickets as
  `<PREFIX>-<n>`. A raw numeric id would not survive that contract.
- **States.** There is no stable status-category field on a work item the way Jira
  exposes `statusCategory.key`. `System.State` is process-specific: Agile calls a
  started item `Active`, Scrum `Committed`, Basic `Doing`. There is also no
  transition graph — `System.State` is written directly.
- **Tags, not labels.** `System.Tags` is one semicolon-delimited string, not a
  collection, so there is no incremental add/remove operation.
- **Rich text.** Description, repro steps, acceptance criteria and comment bodies
  are HTML fragments, not markdown and not Jira's ADF.
- **No MCP.** There is no Azure DevOps MCP in trau's provider surface, so unlike
  Jira and GitHub there is no natural-language fallback path to lean on.

## Decision

Add an `azure` tracker provider backed by a new `internal/tracker/azureapi` REST
client (Work Item Tracking, api-version 7.1), configured by `AZURE_ORG_URL` plus
`AZURE_PAT`, with the team project name reusing `LINEAR_TEAM` exactly as Jira
reuses it for its project key.

1. **Identifiers are prefix-mapped.** Work item 1234 in a `TRAU`-prefixed repo is
   `TRAU-1234` everywhere in trau; the provider parses the trailing number back out
   before every API call. This is the mapping the GitHub provider already applies
   to issue numbers, so no other package changes.
2. **States go through a category, never a name.** `azureapi.Category` maps the
   state names the Agile, Scrum, CMMI and Basic templates ship with onto a
   process-agnostic bucket, and an unrecognized name reports `Unknown` so
   checkpoint reconciliation leaves live work intact rather than guessing. Writes
   resolve the loop's target ("In Review") against the states the project's own
   type declares, falling back by category when the template has no such stage.
3. **REST-only, no fallback.** The PAT is the sole identity, and it needs two
   scopes: Work Items (read & write) for the tickets, and Project and Team (read)
   for the team-project list the wizard's picker and the doctor's live ping read.
   Missing credentials are a doctor **failure**, not the warning Jira gets, because
   there is nothing to degrade to. `buildTracker` drops the agent runner for
   `azure` entirely.
4. **The hub does not sync Azure DevOps.** `NewReader`/`NewWriter` refuse `azure`
   the way they refuse `github`, so the board reports backlog-unavailable and the
   loop reads tickets straight from the tracker. Sync (ADR 0007) needs
   `SyncPull`/`ProjectIdentifiers`/`Identity` semantics that a WIQL-and-batch read
   model can satisfy, but that is a separate slice.

   **Superseded for reads by ADR 0028.** That slice landed: `NewReader` now builds
   an `azureReader` and the hub mirrors the board. `NewWriter` still refuses
   `azure` — filing work items and publishing PRDs from the UI is untouched.
5. **Comments ride on `System.History`.** The dedicated comments route is still
   preview-gated, whereas writing `System.History` through the work-item PATCH is
   GA and rides along on whichever update is already in flight — one round-trip for
   a state change plus its note. Reading the discussion has no GA route at all, so
   that one call pins `api-version=7.1-preview.4` explicitly; every other request
   stays on 7.1.

## Consequences

- A repo sets `TRACKER_PROVIDER=azure`, `LINEAR_TEAM=<team project>`,
  `AZURE_ORG_URL`, `AZURE_PAT` and an `ISSUE_PREFIX`, and the loop picks, drives,
  labels, resets, quarantines and bug-files against Azure Boards.
- `ISSUE_PREFIX` is effectively required for an Azure DevOps repo: with no prefix
  and no key to derive one from, identifiers fall back to `COD-<n>`.
- The PR trailer emits `AB#<id>` rather than a bare identifier, so Azure Boards
  auto-links the work item from the pull request.
- A tag write is a read-modify-write of the whole `System.Tags` string; two
  concurrent label changes on one work item can lose one. The loop only ever
  labels the ticket it owns, so this is accepted rather than locked.
- `Removed`, and a `Completed` item whose `System.Reason` says it was dropped
  (`Cut`, `Duplicate`, `Obsolete`, …), both normalize to canceled — Azure DevOps
  closes discarded and delivered work into the same category.
- The onboarding readiness screen now always has one reachable ticket system,
  since Azure DevOps needs no local tooling. A missing Linear/Jira/GitHub MCP is
  therefore reported as a warning rather than a block, which is accurate: the user
  does have a viable path.
