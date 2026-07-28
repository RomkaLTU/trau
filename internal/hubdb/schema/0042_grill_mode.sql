-- The mode a grilling session runs in: "research" for a session whose deliverable
-- is a findings report, empty or "interview" for the clarify-an-issue default.
-- Locked at create so a resume rebuilds the same first-turn framing; empty keeps
-- every legacy row on the interview prompt.
ALTER TABLE grill_sessions ADD COLUMN mode TEXT NOT NULL DEFAULT '';
