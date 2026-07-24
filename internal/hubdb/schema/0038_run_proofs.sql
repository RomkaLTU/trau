-- Browser-verify proofs: the screenshots and trace a verify run captured while
-- driving the app, harvested to the hub so they survive the /tmp cleanup and are
-- viewable over the API. Screenshot bytes live in the content-addressed blob store
-- (sha256, deduped); a 'video' row records only the local trace directory path,
-- which is not uploaded. Rows key on (repo, ticket) TEXT rather than an issues
-- foreign key because sync wholesale-replaces a repo's synced issues. seq orders a
-- run's proofs (screenshots in manifest order); the latest verify attempt replaces
-- a run's rows, so a resume never appends duplicates.
CREATE TABLE run_proofs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    repo       TEXT NOT NULL,
    ticket     TEXT NOT NULL,
    seq        INTEGER NOT NULL DEFAULT 0,
    kind       TEXT NOT NULL DEFAULT 'screenshot',
    sha256     TEXT NOT NULL DEFAULT '',
    mime       TEXT NOT NULL DEFAULT '',
    caption    TEXT NOT NULL DEFAULT '',
    trace_dir  TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX run_proofs_repo_ticket ON run_proofs(repo, ticket);
CREATE INDEX run_proofs_sha256 ON run_proofs(sha256);
