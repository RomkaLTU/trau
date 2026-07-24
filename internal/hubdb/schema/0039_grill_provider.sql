-- The provider a grilling session runs on, empty when it runs claude. Locked at
-- create (the runner reads the child's stream contract per provider, so it cannot
-- change mid-session); empty keeps every legacy and in-flight session on claude.
ALTER TABLE grill_sessions ADD COLUMN provider TEXT NOT NULL DEFAULT '';
