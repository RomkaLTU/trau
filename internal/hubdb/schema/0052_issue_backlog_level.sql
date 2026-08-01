-- The tracker's own work-item type name ("Epic", "Feature", "User Story", "Bug",
-- "Task") and the normalized level the project's backlog configuration places it
-- on (epic | feature | requirement | task, or '' for a type it places nowhere).
-- Azure DevOps only: every other provider leaves both empty, and there is no
-- backfill for the tickets synced before this migration — the next pull fills them.
ALTER TABLE issues ADD COLUMN work_item_type TEXT NOT NULL DEFAULT '';
ALTER TABLE issues ADD COLUMN backlog_level  TEXT NOT NULL DEFAULT '';
