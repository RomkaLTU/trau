-- Per-ticket git worktrees, the hub's record of the trees runs work in when a
-- repo has WORKTREES=1 (ADR 0044). One row per (repo, ticket): the path is
-- computed deterministically from config on both sides, so the row is a lifecycle
-- record rather than the source of truth for where a tree lives.
--
-- state is the tree's standing:
--   active   — the tree is on disk and a run may be working in it
--   settled  — the ticket finished (merged, reset, requeued, purged) and the tree
--              was removed
--   orphaned — the row outlived its directory: a crash, a manual `git worktree
--              remove`, or a CLI path that removed the tree without the hub
CREATE TABLE worktrees (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    repo       TEXT NOT NULL,
    ticket     TEXT NOT NULL,
    path       TEXT NOT NULL,
    branch     TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT '',
    state      TEXT NOT NULL DEFAULT 'active',
    UNIQUE (repo, ticket)
) STRICT;

CREATE INDEX worktrees_repo ON worktrees(repo);
CREATE INDEX worktrees_state ON worktrees(state);
