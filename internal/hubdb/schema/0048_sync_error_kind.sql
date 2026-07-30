-- What it takes to clear the last recorded sync error: 'config' for a failure the
-- repo's own settings or credentials have to fix, 'rate-limit' or 'transient' for a
-- tracker that is briefly refusing. Empty when no error stands, and on a row stamped
-- before this column existed — which reads as config, what those errors already gated on.
ALTER TABLE issue_sync ADD COLUMN last_error_kind TEXT NOT NULL DEFAULT '';
