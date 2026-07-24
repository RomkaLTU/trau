# ADR 0021 — Skill routing rules are repo-owned and resolved per run

**Status:** Accepted

## Context

ADR 0017's prompt catalog and the phase-aware skill sets that preceded this
decision made every phase name a non-empty skill set, but the set was
ticket-blind: `REQUIRED_SKILLS` forced the same names into every build. In this
repo that pinned 9 of 14 installed skills into every prompt — roughly 19k tokens
of mostly irrelevant skill text per build, with `github-release` loaded for a
web-UI slice.

The inputs to a better decision already exist and already sit on the hub side of
the seam: at build time the loop holds the ticket's title, description and
labels; from verify onward it holds the slice's changed files (the same list that
already picks the workspace app URL and lint-fix command, ADR 0019). Relevance
should therefore be computed by trau — deterministically, from data it has —
rather than delegated to the agent, which empirically loads only the skills a
prompt names for it.

Two things blocked that. Skill identity was the *directory name* and nothing
else: trau never opened `SKILL.md`, so the frontmatter `description` — the one
machine-readable relevance signal a skill ships — was invisible. And there was no
place for a repo to say *when* a skill applies.

## Decision

### 1. Rules live in a repo-owned file, not the config layers

Routing rules live in `<repo>/.trau/skills-rules.json`, one rule per skill:

```json
{
  "rules": [
    { "skill": "golang-code-style", "scope": "always" },
    { "skill": "web-feature", "scope": "auto", "paths": ["web/**"], "keywords": ["web ui"] },
    { "skill": "github-release", "scope": "manual" }
  ]
}
```

A rule is structured — a scope, applicable phases, path globs and keywords — and
the config layers (ADR 0016) parse flat `KEY=value` lines, which the Settings
surface (ADR 0011) renders as flat fields. Encoding a per-skill rule set into one
`KEY=value` string would make it unreadable in `.trau.ini` and uneditable in the
catalog-driven Settings page. `.trau/` is already where a repo keeps structured,
checked-in trau content (`.trau/checks`, ADR 0007-era verify checks), so rules
join it rather than sitting at the repo root next to the skills.sh
`skills-lock.json`, whose file space belongs to that CLI.

The file is repo-owned and checked in: it is a property of the repo, so any
machine running the loop against it resolves the same sets.

`REQUIRED_SKILLS` and `REQUIRED_SKILLS_VERIFY` keep their meaning and their
place in the config layers. Rules decide relevance; the pins remain the escape
hatch that forces a name into a phase regardless.

### 2. Scopes

- **`always`** — named in every phase the rule applies to. This is what
  `REQUIRED_SKILLS` means today.
- **`auto`** — named when the rule's match rules hit.
- **`manual`** — never named automatically. Human-workflow skills (releases,
  ticket bootstrapping) live here; a config pin can still name one.

An unrecognized scope normalizes to `auto`, and an `auto` rule with neither paths
nor keywords can never hit. A half-written or misspelled rule therefore leaves
its skill out of every set rather than forcing it into all of them.

### 3. Match semantics

Rules are evaluated per phase — `build`, `verify`, `repair` (bugfix shares
repair, since both run against the slice's diff). A rule with no `phases` applies
to all three.

- **Paths** are `/`-separated globs. Within a segment the syntax is `path.Match`'s;
  a `**` segment spans any number of segments, so `web/**` covers everything under
  `web/` and `**/*.go` covers Go files at any depth.
- **Keywords** match the ticket's text case-insensitively on word boundaries, so
  a `go` keyword matches "go build" and not "google". Multi-word keywords work
  unchanged.
- **Build** matches keywords against the ticket's title, description, comments
  and labels, and matches path globs against the paths the ticket itself names.
- **Verify, repair and bugfix** match path globs against the slice's changed
  files. A diff that cannot be listed simply matches nothing, so the phase falls
  back rather than fabricating a set.

Globs and keywords are repo-configured. Trau's own defaults name no framework —
the prompts stay framework-agnostic (ADR 0008-era convention).

### 4. Resolution and the never-empty backstop

A phase's set is `always-skills ∪ auto matches ∪ the phase's configured pins`,
with every name intersected against what the repo can actually load. `manual`
skills are excluded from the automatic part but remain loadable by name through a
pin.

That union replaces the first step of the existing fallback chain; the rest of
the chain is unchanged and remains the backstop. When rules and pins both resolve
empty, the phase falls back to the project type's recommended set, then to every
installed skill. A repo that installs skills therefore never renders a
skill-less prompt — a rules file that is missing, empty, unparseable, or that
matches nothing is a fallback, never a regression.

Verify additionally unions its own pins, the installed test-token skills, and
`browser-harness` when browser verify is active; an empty verify union falls
through to the repair set exactly as before.

### 5. Skill metadata

`SKILL.md` frontmatter is parsed for `name` and `description` with a deliberately
tolerant reader: skills come from third-party registries, so a manifest whose
YAML does not parse still yields both fields on a line scan. A skill directory
with no readable frontmatter is reported as **invalid** in the skills inventory
rather than silently counted as a healthy install. Descriptions seed suggested
keywords for `auto` rules and feed the Skills page inventory; nothing routes on a
suggestion until it is saved into a rule.

### 6. Skill scope: repo skills plus a known out-of-repo set

`InstalledSkillNames` scans the repo's skill directories only. Verify already
names `browser-harness`, which loads from outside the repo. That name is now the
whole of an explicit **known out-of-repo skill set**: rules and pins may reference
it, and it never trips the missing-skill warning.

**User-scope skill directories are not enumerated.** A repo's resolved sets must
be a property of the repo, not of the machine running the loop — otherwise the
same ticket resolves different sets on two developers' machines and the planned
sets stop being comparable. A machine-local skill can still be reached by adding
it to the known out-of-repo set, which is a deliberate, reviewed change.

Rules naming a skill outside both sets warn at loop start, the same way a
mistyped `REQUIRED_SKILLS` entry does, and drop out of resolution.

### 7. The web edits rules through the hub

`GET /api/v1/repos/{repo}/skills` returns the inventory (with metadata, validity
and each skill's routing scope), the rules as stored, the rules that name unknown
skills, any parse error, and the **per-phase plan** for a hypothetical run — no
ticket and no diff, so every `auto` rule reads as non-matching and the plan shows
the floor the always-skills, the pins and the chain guarantee.

`PUT /api/v1/repos/{repo}/skills/rules` replaces the rule list and writes the
file. The hub owns the write; the running loop only ever reads. Scopes are
normalized on write and a rule without a skill is rejected.

### 8. Planned sets are recorded from day one

Every phase attempt that names a set emits a `skills_planned` event carrying the
ticket, the phase, the names, and the step that produced them. Paired with the
skills an `agent_call` reports the agent having loaded, this makes plan-versus-
loaded coverage measurable per phase.

This is deliberately observe-first: the receipt lands before any default is
tightened, so the effect of a future change to the defaults is comparable against
a real baseline rather than argued from first principles.

## Consequences

- A slice loads the skills its diff and ticket call for; a web-only slice stops
  paying for release and Go-tooling skill text.
- Relevance is deterministic and reviewable — it is repo content in git, not a
  judgement the agent makes per run.
- Rules are one more repo file to keep honest. The loop-start warning, the
  invalid-manifest flag, and the per-phase plan in the API are what keep a stale
  or mistyped rule visible.
- A repo that adopts no rules behaves exactly as it did before: the pins and the
  fallback chain decide, unchanged.
