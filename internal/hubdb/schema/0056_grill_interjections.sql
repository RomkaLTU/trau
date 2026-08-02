-- The last interjection message the hub has handed to a turn. The user can type
-- while the agent is working, and those messages queue until its next tool call, so
-- delivery is tracked on the row rather than in runner memory: a turn that dies with
-- one queued still delivers it exactly once, across a hub restart.
ALTER TABLE grill_sessions ADD COLUMN interjection_cursor INTEGER NOT NULL DEFAULT 0;
