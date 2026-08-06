-- The per-run skip set a queued item carries (ADR 0037): the canonical keys,
-- comma-separated, applied only to this item's child and never persisted to
-- config. Additive and forward-only: an item queued before this migration
-- carries none and its child runs the whole pipeline.
ALTER TABLE queue_items ADD COLUMN skips TEXT NOT NULL DEFAULT '';
