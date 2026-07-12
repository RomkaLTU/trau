-- Promote the durable per-run phase artifacts — the handoff brief, verify rubric,
-- verify verdict, and build notes — from per-run files (runs/<ticket>/{handoff.md,
-- rubric.json,verdict.json,buildnotes.md}) to an authoritative store keyed by repo,
-- ticket, and kind (ADR 0008 §1). The child posts each artifact to the hub as its
-- phase produces it; a resume restores it from here. The legacy files fold in on the
-- hub's first touch of a repo via hubstore.Artifacts.ImportLegacy.
CREATE TABLE artifacts (
    repo    TEXT NOT NULL,
    ticket  TEXT NOT NULL,
    kind    TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo, ticket, kind)
) STRICT;
