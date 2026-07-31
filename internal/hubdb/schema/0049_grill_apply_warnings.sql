-- What a landed apply could not do but never gated on — the superseded note a
-- detached ticket never received. Stored as a JSON array so a review remounted on
-- the settled session still raises them, long after the apply response is gone.
ALTER TABLE grill_sessions ADD COLUMN apply_warnings TEXT NOT NULL DEFAULT '';
