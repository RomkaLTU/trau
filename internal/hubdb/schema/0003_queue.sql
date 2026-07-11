CREATE TABLE queue_repos (
    root      TEXT PRIMARY KEY,
    draining  INTEGER NOT NULL DEFAULT 0,
    no_resume INTEGER NOT NULL DEFAULT 0,
    on_fault  TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE queue_items (
    root      TEXT NOT NULL,
    position  INTEGER NOT NULL,
    id        TEXT NOT NULL,
    kind      TEXT NOT NULL,
    title     TEXT NOT NULL DEFAULT '',
    status    TEXT NOT NULL,
    reason    TEXT NOT NULL DEFAULT '',
    pid       INTEGER NOT NULL DEFAULT 0,
    queued_at TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (root, id)
) STRICT;

CREATE TABLE queue_sub_issues (
    root     TEXT NOT NULL,
    item_id  TEXT NOT NULL,
    position INTEGER NOT NULL,
    id       TEXT NOT NULL,
    title    TEXT NOT NULL DEFAULT '',
    state    TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (root, item_id, id)
) STRICT;
