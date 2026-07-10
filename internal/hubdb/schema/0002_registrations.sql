CREATE TABLE known_repos (
    root     TEXT PRIMARY KEY,
    name     TEXT NOT NULL,
    runs_dir TEXT NOT NULL
) STRICT;

CREATE TABLE registered_repos (
    id   INTEGER PRIMARY KEY,
    root TEXT NOT NULL UNIQUE
) STRICT;
