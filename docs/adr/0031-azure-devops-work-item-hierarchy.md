# ADR 0031 — Azure DevOps work-item hierarchy is read from the project

- **Status:** Accepted
- **Date:** 2026-08-01
- **Deciders:** Romas (sole maintainer)
- **Ticket:** TRAU-24

## Context

An Azure DevOps board is four levels deep — Epic → Feature → User Story/Bug →
Task — and trau read none of it. `azureapi.WorkItem.Type` carried
`System.WorkItemType` as far as the reader and stopped there: `SyncedIssue` had no
type field, `hubstore.Issue` had no column, and the whole hierarchy in the store
was `Parent` plus `HasChildren`.

Two things broke because of it. `azureStartable` treated any unstarted leaf with
its blockers resolved as work to build, so a Feature nobody had broken down yet —
childless, carrying the ready tag — was indistinguishable from a slice and the
loop built it. And `NewWriter` refused `azure` outright (ADR 0028 §6), so every
hub write on an Azure repo was blocked: no grill create, rewrite, split, comment
or label.

Matching on the literal string `"Feature"` would fix neither properly. Azure
processes are customizable: Agile calls the requirement level "User Story", Scrum
"Product Backlog Item", CMMI "Requirement", and a project may rename its types or
add up to five portfolio backlogs of its own.

## Decision

1. **Levels are read from the project, never hard-coded.** The same reason
   `resolveState` reads the project's own states instead of assuming "Active".
   `GET {org}/{project}/{team}/_apis/work/backlogconfiguration` answers with ranked
   `portfolioBacklogs`, a `requirementBacklog`, a `taskBacklog` and a
   `bugWorkItems` section, and `azureapi.BacklogLevels` collapses that onto the
   four levels trau reasons about:

   | level | what it is |
   |---|---|
   | `epic` | any portfolio backlog above the lowest one |
   | `feature` | the lowest portfolio backlog — the one directly above requirement |
   | `requirement` | the requirement backlog (User Story / PBI / Requirement) |
   | `task` | the taskboard |
   | `""` | a type the configuration places nowhere |

   Naming the lowest portfolio backlog `feature` by its **rank** rather than by its
   name is what makes a renamed or custom process work.

2. **A Bug's level is a team setting.** `bugsBehavior` on
   `GET {org}/{project}/{team}/_apis/work/teamsettings` is `asRequirements`,
   `asTasks` or `off`. trau honours it: a Bug lands at `requirement` or at `task`,
   and a team with bugs off places it nowhere. Both reads resolve the team the same
   way — the first entry of `AZURE_TEAMS` when the repo names teams, otherwise the
   project's default team, which is what the route resolves when the team segment
   is left out. When several teams are named the first wins and the choice is
   logged; a repo mirroring two boards that disagree about where a Bug sits is not
   a case worth modelling.

3. **The store shape is Azure-specific.** Two columns on `issues` —
   `work_item_type` and `backlog_level` — documented as Azure-only and left empty
   by every other provider. No provider-neutral abstraction and no Jira backfill:
   nothing else trau syncs has a typed hierarchy to normalize, and inventing one
   for a single provider would be a shape to maintain rather than a shape to use.
   There is no backfill of rows synced before the migration either — the next pull
   fills them.

4. **The badge shows the raw type name.** `Epic`, `Feature`, `User Story`, `Bug`,
   `Task` — that is what the user recognises from the board. The normalized level
   only drives behaviour, and picks the chip's colour.

5. **The loop refuses anything above requirement level.** `azureStartable` now
   takes the candidate's level and returns false for `epic` and `feature`, whatever
   its children. The build prompt is told where the ticket sits, so an agent
   holding a slice knows it is one.

6. **`NewWriter` builds an `azureWriter`**, reversing ADR 0028 §6. It is the same
   direct-API identity the reader uses — the repo's org URL and PAT, no new scope —
   and it covers create, comment, description, labels and blocker links.
   `PublishDocument` files an issue, the fallback Jira already takes: Azure keeps
   its documents in a wiki, a separate host and a scope the tracker credentials do
   not carry.

7. **trau never creates an Epic or a Feature.** A single create is always
   requirement level — a User Story, or a Bug for a defect — and a Task only ever
   appears as a slice of the item the same apply is breaking down: the story a
   create just filed, or the story a split is specifying. There is no "file a lone
   Task under story X" affordance. A pinned type is honoured only when the project
   places it on the level the draft files at, which is what keeps a create from
   reaching above requirement.

8. **The Feature picker reads the hub's synced mirror**, not a live WIQL query.
   The mirror is already scoped to exactly the slice of the board the loop picks
   from, so the picker cannot offer a Feature the repo does not cover, and it costs
   no PAT budget. Picking none files the work item top-level and leaves the
   re-parenting to the Azure DevOps board.

9. **Assignment stays unsupported.** `AssignIssue` and `AssignableUsers` return
   `ErrUnsupported`: Azure identity search needs the separate Graph host
   (`vssps.dev.azure.com`) and a PAT scope ADR 0024 does not require.
   `ErrUnsupported` is already the documented way for a surface to hide an
   affordance rather than error, so the hub simply offers no assignee on an Azure
   repo.

## Consequences

- A pull costs two more requests: the backlog configuration and the team settings,
  both on the `vso.work` scope the PAT already holds for WIQL. They are read once
  per pull, once per `Pick`, and once per apply.
- Every read path treats the level model as an enrichment and fails the same way.
  A configuration the PAT or the named team cannot read costs the board its badge,
  the prompt its line and the loop its epic/feature gate — never the pull, the pick
  or the prompt itself, each of which worked before trau read levels at all. The
  gate is only as good as the read behind it: a repo that cannot see its own
  backlog configuration picks the way it did before this ADR, and says so in the
  debug log. Filing is the exception — a create cannot place a type it cannot read,
  so the writer refuses by name rather than guessing a level.
- A repo whose process places its requirement backlog nowhere trau can see gets a
  create that refuses by name rather than filing at a guessed level.
- No new configuration. The level model rides on `AZURE_ORG_URL`, `AZURE_PAT`,
  `LINEAR_TEAM` and the optional `AZURE_TEAMS` that ADR 0028 already documents.
- Out of scope and unchanged: filtering the backlog by type (the badge lands
  first), creating Epics or Features from trau, type and level for Jira or Linear,
  and assignment on Azure DevOps.
