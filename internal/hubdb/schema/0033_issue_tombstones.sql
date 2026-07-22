-- Identifiers a repo has hard-deleted. A purge removes every local trace of an
-- issue, which leaves nothing for a later pull to recognize as already-seen; this
-- table is what makes the removal stick — Upsert skips an incoming issue whose
-- (repo, identifier) is listed here, so a ticket the tracker still returns is
-- never re-imported. Distinct from issues.deleted_at, the soft tombstone sync
-- stamps on a ticket that left the Project and clears when it comes back: a row
-- here is permanent. Internal issues need none — nothing re-imports them and
-- issue_seq never reuses an identifier. repo keys on the repo root, as
-- issues.repo does.
CREATE TABLE issue_tombstones (
    repo       TEXT NOT NULL,
    identifier TEXT NOT NULL,
    deleted_at TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo, identifier)
) STRICT;
