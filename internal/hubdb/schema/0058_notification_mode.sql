-- The grilling session type behind a grill_question notification, so clicking it
-- reaches the surface that owns the session: an interview its inbox row, research
-- its report on the Research page. The run kinds leave it empty. Rows written
-- before this column existed carry '' and route as interviews, which is what every
-- notification recorded then was.
ALTER TABLE notifications ADD COLUMN mode TEXT NOT NULL DEFAULT '';
