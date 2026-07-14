-- Web grilling sessions (grilling-prd.md, epic COD-877). The hub spawns a
-- repo-aware agent that interviews the user one question at a time; the session
-- and its transcript survive page closes and hub restarts, ending in a proposal
-- the user reviews. The hub is the sole writer (ADR 0008): the child talks to it
-- only over MCP/HTTP. issue_id is empty for authoring sessions that anchor to the
-- repo alone. session_chain is the latest Claude session id, updated every turn.
CREATE TABLE grill_sessions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    repo          TEXT NOT NULL,
    issue_id      TEXT NOT NULL DEFAULT '',
    state         TEXT NOT NULL DEFAULT 'running',
    session_chain TEXT NOT NULL DEFAULT '',
    model         TEXT NOT NULL DEFAULT '',
    parked_reason TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX grill_sessions_repo_id ON grill_sessions(repo, id);

CREATE TABLE grill_messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL REFERENCES grill_sessions(id) ON DELETE CASCADE,
    role       TEXT NOT NULL DEFAULT '',
    kind       TEXT NOT NULL DEFAULT '',
    payload    TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX grill_messages_session_id ON grill_messages(session_id, id);
