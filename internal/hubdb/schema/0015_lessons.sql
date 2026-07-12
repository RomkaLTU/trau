-- Promote the per-repo lessons ledger — the distilled repair-experiment takeaways a
-- failed or repaired run leaves for later runs (COD-529) — from an append-only
-- per-repo file (runs/memory/lessons.jsonl) to an authoritative store keyed by repo
-- and insertion order (ADR 0008 §1). The child posts each distilled lesson to the
-- hub as verify records it and recalls the relevant ones for prompt injection from
-- here; the legacy file folds in on the hub's first touch of a repo via
-- hubstore.Lessons.ImportLegacy. evidence and tags are JSON arrays.
CREATE TABLE lessons (
    id            INTEGER PRIMARY KEY,
    repo          TEXT NOT NULL,
    ticket        TEXT NOT NULL DEFAULT '',
    phase         TEXT NOT NULL DEFAULT '',
    failure_type  TEXT NOT NULL DEFAULT '',
    attempted_fix TEXT NOT NULL DEFAULT '',
    evidence      TEXT NOT NULL DEFAULT '',
    result        TEXT NOT NULL DEFAULT '',
    lesson        TEXT NOT NULL,
    tags          TEXT NOT NULL DEFAULT '',
    recorded_at   TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX lessons_repo ON lessons(repo, id);
