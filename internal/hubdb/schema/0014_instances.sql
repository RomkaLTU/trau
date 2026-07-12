-- Instance presence: the live loops on this machine, keyed by PID (ADR 0005,
-- ADR 0008 §7). Each loop registers over HTTP on start, refreshes its reported
-- session state on every change and on a heartbeat timer, and deregisters on
-- clean exit — replacing the per-PID ~/.trau/instances/<pid>.json files and the
-- hub's glob-and-reap read. Liveness stays pid-only: the hub probes each PID with
-- signal 0 and reaps a dead one, so a crashed child that never deregistered ages
-- out; heartbeat staleness never reaps a live PID, so a suspended loop keeps its
-- repo-is-live guard. Persisting the rows lets presence survive a hub restart —
-- the still-running children's PIDs stay alive across it — with no visibility gap.
CREATE TABLE instances (
    pid           INTEGER PRIMARY KEY,
    repo_root     TEXT NOT NULL DEFAULT '',
    runs_dir      TEXT NOT NULL DEFAULT '',
    started_at    TEXT NOT NULL DEFAULT '',
    heartbeat     TEXT NOT NULL DEFAULT '',
    session_state TEXT NOT NULL DEFAULT '',
    ticket        TEXT NOT NULL DEFAULT '',
    phase         TEXT NOT NULL DEFAULT '',
    state_since   TEXT NOT NULL DEFAULT ''
) STRICT;
