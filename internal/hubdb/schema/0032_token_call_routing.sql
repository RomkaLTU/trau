-- Record what each agent call actually ran under, so spend can be grouped by the
-- configuration that produced it: the resolved reasoning effort, the call's
-- wall-clock duration (already on the agent_call event, but only inside a JSON
-- blob), and a fingerprint of the routing config in force. Historical rows keep
-- the defaults and an empty config_hash reads as the unknown cohort; there is no
-- backfill.
ALTER TABLE token_calls ADD COLUMN effort TEXT NOT NULL DEFAULT '';
ALTER TABLE token_calls ADD COLUMN duration_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE token_calls ADD COLUMN config_hash TEXT NOT NULL DEFAULT '';

-- The routing fingerprint each repo last ran under, so a run whose fingerprint
-- differs is recognizable as a cohort boundary and marked with a config_change
-- event. keys is the routing-relevant key/value map the hash was computed over,
-- kept so the next change can be reported as a diff. Only routing keys ever land
-- here — no credential participates in the fingerprint.
CREATE TABLE repo_routing (
    repo TEXT PRIMARY KEY,
    hash TEXT NOT NULL DEFAULT '',
    keys TEXT NOT NULL DEFAULT '',
    ts   TEXT NOT NULL DEFAULT ''
) STRICT;
