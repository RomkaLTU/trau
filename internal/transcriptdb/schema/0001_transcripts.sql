CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

-- Agent PTY transcripts as chunked rows keyed by (repo, stem, seq), where stem is
-- the <unix-nano>-<label> transcript-session id the agent names a phase's session
-- (ADR 0008 §4). Appends are cheap and reads range/paginate by seq. ts is the
-- hub's insert time in unix nanoseconds, so the list can order sessions by last
-- activity and retention can prune the least-recently-touched. cols/rows carry the
-- terminal dimensions the session painted at, so replay sizes the emulator before
-- the first byte. data is the raw terminal bytes for the chunk.
CREATE TABLE transcript_chunks (
    repo TEXT NOT NULL,
    stem TEXT NOT NULL,
    seq  INTEGER NOT NULL,
    ts   INTEGER NOT NULL DEFAULT 0,
    cols INTEGER NOT NULL DEFAULT 0,
    rows INTEGER NOT NULL DEFAULT 0,
    data BLOB NOT NULL,
    PRIMARY KEY (repo, stem, seq)
) STRICT;

CREATE INDEX transcript_chunks_repo_ts ON transcript_chunks(repo, ts);
