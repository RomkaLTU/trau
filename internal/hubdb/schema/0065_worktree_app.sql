-- The app a worktree serves. With APP_START_CMD set, the hub runs the target app
-- inside each tree on its own port, so every lane has a live localhost URL for
-- browser verify to drive instead of the human's dev server on the registered
-- checkout (COD-1584).
--
-- app_state is the app's standing:
--   stopped  — nothing is running for this tree (the default, and where a
--              removal, a dead pid at boot reconcile, or a repo with no
--              APP_START_CMD leaves it)
--   starting — the command was spawned and the port has not accepted a
--              connection yet
--   running  — the port accepted, so the URL is live
--   failed   — the command never listened within the readiness window, or would
--              not spawn at all; its captured output is kept for inspection
--
-- port is the port allocated to the tree, held for as long as the app is live so
-- a second tree is handed the next free one.
ALTER TABLE worktrees ADD COLUMN port INTEGER NOT NULL DEFAULT 0;
ALTER TABLE worktrees ADD COLUMN app_pid INTEGER NOT NULL DEFAULT 0;
ALTER TABLE worktrees ADD COLUMN app_state TEXT NOT NULL DEFAULT 'stopped';
ALTER TABLE worktrees ADD COLUMN app_started_at TEXT NOT NULL DEFAULT '';
