-- Promote token calls from the rebuildable derived projection to an authoritative,
-- migrated table (ADR 0008 §2), and add the anomalies table beside it. Children now
-- POST token calls and flagged cost anomalies to the hub; the per-run tokens.jsonl /
-- anomalies.jsonl files and the file-tailing ingest (with its ingest_sources
-- byte-offset cursors) are gone.
--
-- Pre-#199 databases already carry a derived `token_calls` keyed by file byte offset
-- (repo, ticket, seq). Ensure that shape exists (a no-op there, a fresh empty table
-- otherwise), fold its rows into the authoritative table under freshly assigned ids
-- that preserve each ticket's append order, then drop the derived cursor table.
CREATE TABLE IF NOT EXISTS token_calls (
    repo           TEXT NOT NULL,
    ticket         TEXT NOT NULL,
    seq            INTEGER NOT NULL,
    ts             TEXT NOT NULL DEFAULT '',
    phase          TEXT NOT NULL DEFAULT '',
    input          INTEGER NOT NULL DEFAULT 0,
    output         INTEGER NOT NULL DEFAULT 0,
    cache_read     INTEGER NOT NULL DEFAULT 0,
    cache_creation INTEGER NOT NULL DEFAULT 0,
    reasoning      INTEGER NOT NULL DEFAULT 0,
    total          INTEGER NOT NULL DEFAULT 0,
    cost_usd       REAL,
    turns          INTEGER NOT NULL DEFAULT 0,
    is_error       INTEGER NOT NULL DEFAULT 0,
    provider       TEXT NOT NULL DEFAULT '',
    model          TEXT NOT NULL DEFAULT '',
    context        INTEGER NOT NULL DEFAULT 0,
    skills         TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo, ticket, seq)
) STRICT;

ALTER TABLE token_calls RENAME TO token_calls_legacy;

CREATE TABLE token_calls (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    repo           TEXT NOT NULL,
    ticket         TEXT NOT NULL,
    ts             TEXT NOT NULL DEFAULT '',
    phase          TEXT NOT NULL DEFAULT '',
    input          INTEGER NOT NULL DEFAULT 0,
    output         INTEGER NOT NULL DEFAULT 0,
    cache_read     INTEGER NOT NULL DEFAULT 0,
    cache_creation INTEGER NOT NULL DEFAULT 0,
    reasoning      INTEGER NOT NULL DEFAULT 0,
    total          INTEGER NOT NULL DEFAULT 0,
    cost_usd       REAL,
    turns          INTEGER NOT NULL DEFAULT 0,
    is_error       INTEGER NOT NULL DEFAULT 0,
    provider       TEXT NOT NULL DEFAULT '',
    model          TEXT NOT NULL DEFAULT '',
    context        INTEGER NOT NULL DEFAULT 0,
    skills         TEXT NOT NULL DEFAULT ''
) STRICT;

INSERT INTO token_calls(
    repo, ticket, ts, phase, input, output, cache_read, cache_creation,
    reasoning, total, cost_usd, turns, is_error, provider, model, context, skills)
SELECT repo, ticket, ts, phase, input, output, cache_read, cache_creation,
       reasoning, total, cost_usd, turns, is_error, provider, model, context, skills
FROM token_calls_legacy ORDER BY repo, ticket, seq;

DROP TABLE token_calls_legacy;
DROP TABLE IF EXISTS ingest_sources;

CREATE INDEX token_calls_repo_ticket ON token_calls(repo, ticket);

CREATE TABLE token_anomalies (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    repo     TEXT NOT NULL,
    ticket   TEXT NOT NULL,
    ts       TEXT NOT NULL DEFAULT '',
    phase    TEXT NOT NULL DEFAULT '',
    output   INTEGER NOT NULL DEFAULT 0,
    turns    INTEGER NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0,
    reasons  TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX token_anomalies_repo_ticket ON token_anomalies(repo, ticket);
