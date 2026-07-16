# ADR 0014 — Assignees are reflected read-only; "Me" is derived from tracker credentials

- **Status:** Accepted
- **Date:** 2026-07-16
- **Deciders:** Romas (sole maintainer)

## Context

trau syncs issues from Linear and Jira into the hub database (ADR 0007)
with no notion of people — no struct field, no query field, no column. The
only person-data in the system is comment author display names. The web
backlog is gaining assignee display, an assignee filter facet, and
search-by-name, which forces two modeling questions: what *is* a person in
trau, and how does the hub know which person the operator is ("Me")?
Credentials are per-repo layered (`.trau.ini` project < user), so one
machine legitimately runs different tracker accounts per Repo.

## Decision

**Assignee is a value on the issue, not an entity.** The `issues` table
gains `assignee_id` (Linear user id / Jira accountId) and `assignee_name`
(display name); NULL means Unassigned. There is no users table and no
workspace member-list sync — the filter facet is a DISTINCT over issues,
exactly like the labels facet. Internal issues are always Unassigned.

**"Me" is derived from the credentials, per Repo binding.** Alongside each
sync cycle the hub asks the tracker who the credentials belong to — Linear
`viewer`, Jira `GET /myself` (canonical `accountId`) — and persists the
answer with the sync bookkeeping. There is no identity config key. Identity
resolution failure is non-fatal: issues still sync; Me-affordances degrade.

**Identity never leaves the hub.** API payloads carry a per-row
`assignee: {id, name, me}`; the facet endpoint flags its Me row; the
`assignee=me` filter token resolves server-side. The client never learns
who the operator is — it only renders booleans.

**Reflection is one-way.** Assignee is never written back to the tracker,
and assignment does not influence the picker, queue, or loop. Sync
overwrites to NULL when a ticket is unassigned upstream — the upsert must
not coalesce old assignees into place.

**No avatar URLs.** Jira Cloud avatar URLs sit behind auth, so hotlinking
fails in the browser; surfaces render initials avatars for both trackers.
An avatar URL column can be added later without ceremony.

## Considered options

- **Synced users table + FK** — a whole new sync surface (pagination,
  tombstones, permissions) for zero v1 benefit; the facet only needs people
  who actually have issues in the Repo's Project.
- **Explicit identity config** (`TRACKER_ME` / `LINEAR_USER_ID`) — redundant
  with credentials that already are an identity, and it drifts. Matching
  Jira users by email is unreliable anyway: Atlassian privacy settings hide
  emails, so `/myself` → accountId is the only robust path.
- **Behavioral coupling** (picker honors assignee, trau assigns itself) —
  deliberately out of scope; it changes run-eligibility semantics and
  deserves its own decision.

## Consequences

- A Repo whose API key belongs to a service account shows the bot as Me.
  Accepted: Me is defined as "whoever the credentials are"; an override key
  can be added if that setup appears.
- Search-by-name requires rebuilding the FTS table to index
  `assignee_name`.
- Glossary gains **Assignee**, **Unassigned**, and **Me** ("Me" is the
  canonical copy; "You" never appears).
