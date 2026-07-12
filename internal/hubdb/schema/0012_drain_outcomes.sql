-- The queue drainer settles a finished child's item from the child's own exit
-- outcome — a fault or provider pause the child hit, or a clean finish (empty
-- class). The child posts that outcome to the hub over HTTP as it exits instead
-- of leaving a .drain-report file under the runs dir (ADR 0008). A row's presence
-- is the load-bearing signal: a dead child that never posted leaves no row, so
-- the drain classifies the outcome unknown and pauses rather than settling done.
-- The drainer clears a ticket's row before it respawns it, so a stale outcome
-- never settles the next attempt.
CREATE TABLE drain_outcomes (
    repo   TEXT NOT NULL,
    ticket TEXT NOT NULL,
    class  TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo, ticket)
) STRICT;
