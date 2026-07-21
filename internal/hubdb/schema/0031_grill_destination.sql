-- The destination a create-apply anchored its parent in: "tracker" for the
-- repo's external tracker, "internal" for the hub issue store. Additive and
-- forward-only: an anchor recorded before this migration carries none and
-- resolves to the repo's own tracker at apply time, so a retry keeps reusing
-- the issue it filed.
ALTER TABLE grill_sessions ADD COLUMN issue_destination TEXT NOT NULL DEFAULT '';
