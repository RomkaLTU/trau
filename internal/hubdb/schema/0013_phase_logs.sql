-- Promote the per-phase agent logs the TUI log inspector browses — one final
-- output per phase, previously written as runs/<ticket>/<phase>.log — to an
-- authoritative store keyed by repo, ticket, and phase (ADR 0008). The child
-- posts each phase's log to the hub as the phase produces it; the inspector reads
-- them back over HTTP instead of listing and reading the run directory.
-- updated_at (unix nanoseconds) preserves the file-era ordering: the inspector
-- shows the most recently written phase first. The legacy files fold in on the
-- hub's first touch of a repo via hubstore.PhaseLogs.ImportLegacy.
CREATE TABLE phase_logs (
    repo       TEXT NOT NULL,
    ticket     TEXT NOT NULL,
    phase      TEXT NOT NULL,
    content    TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (repo, ticket, phase)
) STRICT;
