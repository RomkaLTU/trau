-- Pre-#199 databases carry a rebuildable derived `checkpoints` projection; drop
-- it so this authoritative table takes its place. Its rows are re-folded from the
-- on-disk state files by hubstore.Checkpoints.ImportLegacy.
DROP TABLE IF EXISTS checkpoints;

CREATE TABLE checkpoints (
    repo           TEXT NOT NULL,
    ticket         TEXT NOT NULL,
    phase          TEXT NOT NULL DEFAULT '',
    title          TEXT NOT NULL DEFAULT '',
    branch         TEXT NOT NULL DEFAULT '',
    pr             TEXT NOT NULL DEFAULT '',
    pr_url         TEXT NOT NULL DEFAULT '',
    failure_reason TEXT NOT NULL DEFAULT '',
    updated_at     TEXT NOT NULL DEFAULT '',
    data           TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY (repo, ticket)
) STRICT;
