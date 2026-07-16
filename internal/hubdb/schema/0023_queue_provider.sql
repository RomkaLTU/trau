-- The per-run Provider override a queued item carries (ADR 0015): applied only
-- to this item's child, never persisted to config. Additive and forward-only:
-- an item queued before this migration carries none and the child runs on the
-- config default.
ALTER TABLE queue_items ADD COLUMN provider TEXT NOT NULL DEFAULT '';
