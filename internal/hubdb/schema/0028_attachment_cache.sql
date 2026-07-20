-- Cache bookkeeping for the attachment store. last_served_at is when a row's bytes
-- were last handed out — the recency the cache cap evicts on, so the coldest
-- tracker files go first and re-download lazily when something wants them again.
-- last_attempt_at is when a fetch was last tried, cached or failed alike, and
-- floors how often a failed row may go back to the tracker. Both are empty until
-- the row's bytes are first fetched.
ALTER TABLE attachments ADD COLUMN last_served_at  TEXT NOT NULL DEFAULT '';
ALTER TABLE attachments ADD COLUMN last_attempt_at TEXT NOT NULL DEFAULT '';

CREATE INDEX attachments_state_served ON attachments(state, last_served_at);
