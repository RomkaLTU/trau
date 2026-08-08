-- The challenger providers a grilling session takes a second opinion from: a CSV of
-- provider names, each of which drafts its own outcome from the interview transcript
-- once the interviewer proposes one. Locked at create like provider and mode, so the
-- draft phase a resume reaches is the one the session opened with; empty keeps every
-- legacy and in-flight session on the solo interview.
ALTER TABLE grill_sessions ADD COLUMN challengers TEXT NOT NULL DEFAULT '';
