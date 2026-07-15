-- The issue source a queued item was resolved from at enqueue time (ADR 0007):
-- internal for a hub-only issue, otherwise the tracker provider. Additive and
-- forward-only: an item queued before this migration carries none and the API
-- omits it.
ALTER TABLE queue_items ADD COLUMN source TEXT NOT NULL DEFAULT '';
