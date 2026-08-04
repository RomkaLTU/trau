-- A title the user gave a research report by hand. The one the agent proposed is not
-- always right, and a report the research page keeps forever is read long after the
-- session that wrote it, so the override wins over the outcome title and survives
-- every later outcome — only a rename ever writes it. Empty keeps the derived title.
ALTER TABLE grill_sessions ADD COLUMN title TEXT NOT NULL DEFAULT '';
