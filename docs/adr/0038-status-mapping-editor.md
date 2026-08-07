# ADR 0038 — Status mapping is edited against the tracker's own vocabulary, behind one provider-agnostic options endpoint

- **Status:** Accepted
- **Date:** 2026-08-07
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-1535

## Context

ADR 0036 made `AZURE_BOARD_STATES` the grouping key for an Azure DevOps repo:
comma-separated `<board column>=<group>` pairs, authoritative and exhaustive
where a repo sets one. It is the right model and the wrong thing to type. An
operator has to know the exact column names their team's boards carry — including
the ones only the Features board shows — spell them into a comma/equals grammar
by hand, and get no feedback at all until the next sync pull regroups the board
the wrong way. The `STATUS_*` pins have the same shape of problem against a
different vocabulary: they name a *work-item state*, not a column, and nothing in
the settings page said so.

Both keys already render in Settings as generic text rows, because ADR 0011 keeps
clients to pure presentation and the catalog has no way to say "this key's values
come from the repo's tracker". The catalog cannot know them: they are per-board,
per-project, and only the PAT can read them.

## Decision

**One endpoint answers "what may this repo's status mapping say", per repo.**
`GET /api/v1/repos/{repo}/tracker/status-options` returns two lists in a shape
that is deliberately provider-agnostic — `grouping` (the columns a mapping keys
on, each with a `suggestedGroup`) and `pinOptions` (the statuses a `STATUS_*` pin
may name, with the category the provider files each under). A provider with no
mapping editor answers **404**, which is an answer and not a failure: the
settings page renders its generic rows and nothing else changes. Later providers
add an arm, never a surface of their own.

**A credential or network failure keeps the 200** and carries `error` and `hint`
— the remediation-friendly shape the tracker connection test already answers with.
A lapsed PAT is not "this provider has no editor", and the editor still has the
repo's own mapping to fall back on, so conflating the two would strand the
operator on a page that offers no way to fix the thing that is broken.

**The suggestion is derived, never invented.** A column's `suggestedGroup` comes
from its own `stateMappings` run through the same category path an unmapped repo
already groups by (ADR 0033), so a prefilled editor proposes exactly what the
board shows today. A column whose work-item types disagree takes the least
advanced of their groups; one mapping nothing recognizable falls back to the rung
Azure DevOps files the column on itself (`incoming`/`inProgress`/`outgoing`).
Columns are unioned across every board of every team in `AZURE_TEAMS` and
deduplicated by name, keeping board order: a project's Stories and Features boards
share most of their columns, and the mapping grammar has one namespace.

**The editor is a third catalog renderer, and it writes through the same path.**
ADR 0011's client-side presentation list gains a status-mapping renderer beside
the routing matrix and the theme grid: the *Tracker & issues* section's advanced
body renders it above that section's remaining generic rows, with
`AZURE_BOARD_STATES` and the four `STATUS_*` keys filtered out of them. Writes are
ordinary repo-config PUTs with the same layer and write-target semantics every
other row uses — no bespoke write path, so unset, shadowing and layer choice keep
behaving as they do everywhere else.

**Prefill is not a write.** With the key empty every select shows its suggestion
and nothing has been written, because empty means "group by category" and that is
a real, working configuration. The moment the operator saves, the mapping is
exhaustive by ADR 0036, so the editor shows every unmapped row resolving to
*Other* before the save rather than letting the gap appear on the board a sync
later. A free-text add row covers a column the API missed, and a name carrying
`,` or `=` is refused inline — the grammar spends both characters.

## Considered options

- **Teach the catalog to carry the options.** `KeyMeta.Options` is static and
  process-wide; these values are per repo, per board, and behind a PAT. The
  catalog would have to grow a fetch, which is what ADR 0011 kept out of it.
- **Reuse the test-connection endpoint.** It exists to validate typed credentials
  before anything is stored, is POST-with-secrets, and is gated by the
  registration exposure check. A read of an already-configured repo's board is a
  different operation with a different failure vocabulary.
- **Return 502 on a bad PAT.** Makes the client parse status codes to tell
  "unsupported" from "misconfigured", and pushes it toward treating both as "hide
  the editor" — the one behavior that leaves the operator no way out.
- **Let pins pick board columns too.** Azure DevOps refuses a
  `System.BoardColumn` write (ADR 0036), so a pin naming a column would fail at
  the first transition. The two lists stay separate vocabularies on purpose.

## Consequences

- Opening the *Tracker & issues* advanced body on an Azure repo now costs a
  backlog-configuration read, a work-item-type states read, and a boards+columns
  read per team. They are cached for the query's stale window only; the body is
  collapsed by default, so a settings visit that never expands it costs nothing.
- A repo that renames a board column keeps the old name in its mapping as a row
  marked *not on the board* rather than dropping it, so saving cannot silently
  discard a mapping the operator has not decided about yet.
- A saved mapping joins `scopeKey` (ADR 0036), so the board regroups on the next
  sync pull rather than immediately. The editor says so after every save, because
  the alternative is an operator concluding the write did not land.
- The `grouping`/`pinOptions` shape is now a compatibility surface: the Linear and
  Jira slices fill the same fields, and the onboarding step reuses the same
  endpoint rather than growing a parallel one.
- The Linear arm (COD-1536) filled it first, and it did **not** inherit the
  exhaustive rule: `LINEAR_BOARD_STATES` overlays the grouping a workflow state's
  own Linear type derives, so the editor prefills every row with its effective
  section and saves only the rows that differ. ADR 0039 records why the two keys
  share a grammar but not that rule.
- The Jira arm (COD-1537) followed the Linear one: `JIRA_BOARD_STATES` overlays
  the grouping a status's own `statusCategory` derives, keyed on the status name
  the project reports. It carries one extra rule Linear has no analogue for — a
  listed status also displaces the won't-do/duplicate *resolution* nuance, since
  naming a status by hand is a statement about every issue in it, while the
  suggestion the editor prefills reads the category alone. That makes a pair
  equal to its own suggestion meaningful rather than redundant, so the editor
  models the section as *conditionally derived* — a done-category row carries a
  **derived/mapped** badge the operator can flip, and the minimal-write rule the
  Linear arm introduced keeps such a pair instead of filtering it out.
- The onboarding slice (COD-1538) needed the same two lists for a repo that does
  not exist yet: during `/projects/new` the credentials live only in the wizard's
  form and there is no stored config for a repo-scoped GET to read. It answers
  with a sibling **`POST /api/v1/trackers/{provider}/status-options`** that takes
  the connection test's credential payload plus the chosen binding and shares the
  repo-scoped handler's provider arms verbatim — the same 200-with-`error` shape,
  the same 404 gate, the same registration exposure check the test-connection
  probe carries because it too receives raw secrets. The editor moved behind
  props so neither half of it knows which endpoint answered; it is the shape,
  not the source, that the ADR fixes.
- The wizard's section is optional and collapsed, fetching only on first expand,
  and it writes nothing unless a row was actually changed or a pin actually
  named. The four `STATUS_*` pins joined `trackerConfigKeys` so they can ride the
  project-tracker write atomically with the rest of the set: they describe the
  tracker's workflow rather than one repo's taste, and `projectSeededKeys`
  already lets a repo-explicit value win over the seed, so a repo that pins its
  own is unaffected.
