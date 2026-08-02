-- Batches: the named, persistent subsets of a repo's queue a drain can be scoped
-- to. The batch row only names the grouping; membership lives on the item, so
-- dismissing a batch dissolves the grouping without touching the work. The scope
-- sits on the armed queue beside `draining`, which is what makes a scoped drain
-- survive a hub restart. Additive and forward-only: a queue from before this
-- migration carries no batches and drains whole.
CREATE TABLE queue_batches (
    root       TEXT NOT NULL,
    id         TEXT NOT NULL,
    name       TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    PRIMARY KEY (root, id)
) STRICT;

ALTER TABLE queue_items ADD COLUMN batch TEXT NOT NULL DEFAULT '';

ALTER TABLE queue_repos ADD COLUMN batch TEXT NOT NULL DEFAULT '';
