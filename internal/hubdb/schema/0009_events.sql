-- Promote events from the rebuildable derived projection to an authoritative,
-- migrated table (ADR 0008 §2). Children now POST events to the hub, which
-- appends them here; the file tail and its byte-offset cursor are gone, so the
-- feed's resume cursor moves onto a real monotonic id.
--
-- Pre-#199 databases already carry a derived `events` table with the ingest era's
-- history keyed by byte offset. Ensure that shape exists (a no-op there, a fresh
-- empty table otherwise), then fold its rows into the authoritative table under
-- freshly assigned ids that preserve each repo's order.
CREATE TABLE IF NOT EXISTS events (
    repo   TEXT NOT NULL,
    seq    INTEGER NOT NULL,
    ts     TEXT NOT NULL DEFAULT '',
    kind   TEXT NOT NULL DEFAULT '',
    phase  TEXT NOT NULL DEFAULT '',
    msg    TEXT NOT NULL DEFAULT '',
    fields TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo, seq)
) STRICT;

ALTER TABLE events RENAME TO events_legacy;

CREATE TABLE events (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    repo   TEXT NOT NULL,
    ts     TEXT NOT NULL DEFAULT '',
    kind   TEXT NOT NULL DEFAULT '',
    phase  TEXT NOT NULL DEFAULT '',
    msg    TEXT NOT NULL DEFAULT '',
    fields TEXT NOT NULL DEFAULT ''
) STRICT;

INSERT INTO events(repo, ts, kind, phase, msg, fields)
SELECT repo, ts, kind, phase, msg, fields FROM events_legacy ORDER BY repo, seq;

DROP TABLE events_legacy;

CREATE INDEX events_repo_id ON events(repo, id);
