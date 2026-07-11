CREATE TABLE issues (
    id           INTEGER PRIMARY KEY,
    repo         TEXT NOT NULL,
    source       TEXT NOT NULL,
    identifier   TEXT NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT '',
    status_group TEXT NOT NULL DEFAULT '',
    priority     INTEGER NOT NULL DEFAULT 0,
    labels       TEXT NOT NULL DEFAULT '[]',
    parent       TEXT NOT NULL DEFAULT '',
    has_children INTEGER NOT NULL DEFAULT 0,
    due_date     TEXT NOT NULL DEFAULT '',
    external_id  TEXT NOT NULL DEFAULT '',
    url          TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL DEFAULT '',
    updated_at   TEXT NOT NULL DEFAULT '',
    synced_at    TEXT NOT NULL DEFAULT '',
    UNIQUE (repo, identifier)
) STRICT;

CREATE TABLE issue_comments (
    id          INTEGER PRIMARY KEY,
    issue_id    INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    external_id TEXT NOT NULL,
    author      TEXT NOT NULL DEFAULT '',
    body        TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT '',
    updated_at  TEXT NOT NULL DEFAULT '',
    UNIQUE (issue_id, external_id)
) STRICT;

CREATE TABLE issue_sync (
    repo           TEXT PRIMARY KEY,
    team_id        TEXT NOT NULL DEFAULT '',
    project_id     TEXT NOT NULL DEFAULT '',
    project        TEXT NOT NULL DEFAULT '',
    cursor         TEXT NOT NULL DEFAULT '',
    last_synced_at TEXT NOT NULL DEFAULT '',
    last_issues    INTEGER NOT NULL DEFAULT 0,
    last_comments  INTEGER NOT NULL DEFAULT 0,
    last_error     TEXT NOT NULL DEFAULT ''
) STRICT;
