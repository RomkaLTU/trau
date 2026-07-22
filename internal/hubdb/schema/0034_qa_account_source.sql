-- How a QA account came to be stored: "manual" for one a person entered in
-- settings, "agent" for one the verifier discovered inside the repo under test
-- and the loop captured. Stamped at creation and left alone by every update, so
-- editing a captured account never rewrites its provenance. Additive and
-- forward-only: an account stored before this migration reads as manual.
ALTER TABLE qa_accounts ADD COLUMN source TEXT NOT NULL DEFAULT 'manual';
