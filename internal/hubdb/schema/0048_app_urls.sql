-- Per-repo browser-verify targets, the hub-managed replacement for the config
-- APP_URL / APP_URLS keys (ADR 0026). One row per app the verifier can drive:
-- the url is the target, the label is for the humans reading the settings
-- surface, and the workspace names the part of a monorepo the app serves. The
-- row with an empty workspace is the repo's default target — today's APP_URL —
-- and uniqueness on (repo, workspace) is what keeps it single while also
-- keeping one entry per workspace.
CREATE TABLE app_urls (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    repo       TEXT NOT NULL,
    label      TEXT NOT NULL DEFAULT '',
    url        TEXT NOT NULL,
    workspace  TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT '',
    UNIQUE (repo, workspace)
) STRICT;

CREATE INDEX app_urls_repo ON app_urls(repo);
