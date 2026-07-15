-- An issue's tracker Assignee (ADR 0014): the assignee's stable tracker id and
-- display name, both NULL when the issue is Unassigned. Nullable, not '' — an
-- unassigned issue has no assignee, and the sync upsert overwrites to NULL rather
-- than coalescing a stale one back into place. Internal issues stay NULL/Unassigned.
ALTER TABLE issues ADD COLUMN assignee_id   TEXT;
ALTER TABLE issues ADD COLUMN assignee_name TEXT;

-- The repo binding's resolved Me identity — the tracker user behind its
-- credentials — refreshed each sync cycle beside the sync bookkeeping. Empty until
-- first resolved; an identity call that fails leaves the previous value intact.
ALTER TABLE issue_sync ADD COLUMN me_id          TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_sync ADD COLUMN me_name        TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_sync ADD COLUMN me_resolved_at TEXT NOT NULL DEFAULT '';
