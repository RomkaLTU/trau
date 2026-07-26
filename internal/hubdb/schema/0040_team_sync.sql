-- Give the hub a read side for the records teammates publish over the repo's own
-- git remote (config TEAM_SYNC), plus the bookkeeping the settings surface and
-- `trau doctor` report a sync's health from. team_records holds each teammate's
-- fetched snapshot verbatim, keyed by writer id, and is replaced wholesale on
-- every fetch — the remote ref is always the truth about what a writer shares, so
-- there is nothing to merge and nothing to tombstone. Locally recorded lessons
-- stay in the lessons table and are never mutated by sync; the two are unioned at
-- read time. team_sync_state carries the id this machine publishes under and the
-- last pass's outcome, so a blameless failure is reportable without ever having
-- surfaced to the run.
CREATE TABLE team_records (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    repo         TEXT NOT NULL,
    writer_id    TEXT NOT NULL,
    author_name  TEXT NOT NULL DEFAULT '',
    author_email TEXT NOT NULL DEFAULT '',
    updated_at   TEXT NOT NULL DEFAULT '',
    fetched_at   TEXT NOT NULL DEFAULT '',
    payload      TEXT NOT NULL,
    UNIQUE(repo, writer_id)
) STRICT;

CREATE INDEX team_records_repo ON team_records(repo, id);

CREATE TABLE team_sync_state (
    repo         TEXT PRIMARY KEY,
    writer_id    TEXT NOT NULL DEFAULT '',
    last_sync_at TEXT NOT NULL DEFAULT '',
    last_error   TEXT NOT NULL DEFAULT ''
) STRICT;
