-- The Atlas store: agent-generated architecture View documents per repo (ADR
-- 0013). Each generation appends a row under a per-(repo, view) version counter,
-- carrying the commit the document was derived from, the validated document JSON,
-- the generation cost, and — for a failed generation — its error text in place of
-- a document. The latest row whose error is empty is the good document that
-- surfaces; a failed row is kept as history without displacing it. Retention keeps
-- the last 10 versions per (repo, view), pruned on insert.
CREATE TABLE atlas_documents (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    repo       TEXT NOT NULL,
    view_id    TEXT NOT NULL,
    version    INTEGER NOT NULL,
    commit_sha TEXT NOT NULL DEFAULT '',
    document   TEXT NOT NULL DEFAULT '',
    cost_usd   REAL NOT NULL DEFAULT 0,
    error      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT '',
    UNIQUE (repo, view_id, version)
) STRICT;
