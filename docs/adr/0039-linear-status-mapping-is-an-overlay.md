# ADR 0039 — Linear's status mapping overlays the type-derived grouping instead of replacing it

- **Status:** Accepted
- **Date:** 2026-08-07
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-1536
- **Companion to:** ADR 0036 (Azure DevOps board columns), ADR 0038 (status mapping editor)

## Context

`LINEAR_BOARD_STATES` extends status mapping to Linear: the same
comma-separated `<name>=<group>` grammar `AZURE_BOARD_STATES` uses, the same
five groups, the same editor. The open question was what an *unlisted* name
means, and copying Azure DevOps's answer would have been wrong.

`AZURE_BOARD_STATES` is exhaustive because it has to be. Azure DevOps board
columns carry no lifecycle metadata of their own worth trusting: two columns
routinely share a state, `System.BoardColumn` is the only thing that tells them
apart, and the fallback the mapping displaces (state categories, ADR 0033) is a
*different* vocabulary — one that cannot answer a per-column question at all. A
half-written mapping there is genuinely ambiguous, so ADR 0036 made the omission
loud: an unlisted column groups as unknown and shows up under *Other*.

Linear is the opposite case. Every workflow state carries a type of Linear's own
— `triage | backlog | unstarted | started | completed | canceled`, plus
`duplicate`, which Linear's docs omit but every team's Duplicate state reports —
that already maps cleanly onto trau's groups, and it is the same vocabulary the
mapping speaks: a Linear board's columns *are* its workflow states. The automatic
grouping is therefore never ambiguous and never absent, and a mapping exists only
to disagree with it (a team whose "Ready for QA" is typed `started` but should
file under *Done* for the loop's purposes).

The type vocabulary is Linear's to extend, and `duplicate` is the proof: trau
read it off a live team after shipping a mapping that did not know it. So the
type mapping has a floor rather than an escape hatch — a type trau does not
recognize groups as *backlog*, the reading that costs the least when it is wrong,
never as unknown.

Linear teams also add states freely, and nothing tells trau when they do. Under
an exhaustive rule, every state created after a mapping was written would file
under *Other* until someone edited config — silently, on a board the operator did
not touch.

## Decision

**`LINEAR_BOARD_STATES` is an overlay.** A state the key names takes its mapped
group; a state it does not name keeps the group its Linear state type derives.
There is no path by which a Linear row groups as unknown — not through the
mapping, and not through a state type trau has never seen — so a state created
later degrades to a real section rather than off the board. Deleting the key
restores pure type-derived grouping — the same thing an empty key means.

**The grammar, the vocabulary and the parser are shared.** Both keys parse
through one `parseStatusMapping` (trimmed, case-folded names; groups drawn from
one `mappableGroups` list; a pair naming an unknown group dropped with a debug
line rather than failing the value). Only the *lookup* differs, which is exactly
the thing that is provider-specific.

**The overlay applies everywhere a Linear group is derived** — the backlog read,
the sync pull, and the `IssueStatus` reconcile read — so a `--status` pass and the
board never disagree about the same ticket. And, like Azure DevOps's, the parsed
mapping's canonical key joins the Linear pull's scope key: two repos on one team
share a coalesced pull only when they map its states identically.

**The editor saves the difference, not the state of the world.** Every row
prefills with its effective section (override, else the type-derived one), and a
save serializes only the rows that differ from their derived default. The key
stays minimal, no row displays *Unknown* — the suggestion a row prefills from is
the same floored type mapping the board groups by, so there is none to display —
and putting every row back on its suggestion writes the empty value, which is the
same configuration as never having written one.

## Considered options

- **Make it exhaustive, like Azure DevOps.** One rule for both providers, at the
  cost of the failure mode the overlay exists to avoid: a new Linear state
  silently landing under *Other* on a board nobody edited. The consistency is
  cosmetic — the two keys answer different questions about different data.
- **Key off the state *type* rather than the state name.** Six pairs would cover
  any team, but it cannot express the case the key exists for: one `started`
  state grouping differently from another `started` state.
- **A separate `LINEAR_STATE_OVERRIDES` name to make the difference obvious.**
  Rejected: the parallel `*_BOARD_STATES` name is what makes the editor, the
  catalog and the docs read as one feature. The description, `trau.ini.example`
  and this ADR carry the distinction instead.

## Consequences

- The two keys look identical and behave differently on the one point that
  matters. Every place that documents them says so prominently — the catalog
  description, `trau.ini.example`, and the editor's own block description.
- The editor's serialized value is not the editor's visible state, so the write
  preview reads `will write:` explicitly rather than letting the two be confused.
- A Linear repo now pays one `workflowStates` read (plus the team lookup) when the
  *Tracker & issues* advanced body is expanded. The body is collapsed by default.
- `Config.LinearStates` travels the same reader seam `BoardStates` does, so the
  hub's board and the loop's own reads always group a repo the same way.
- The reconcile read stopped carrying its own copy of the type vocabulary:
  `mapLinearState` resolves through `mapLinearGroup`, so a type Linear adds is
  learned once and the two readings cannot drift apart.
