-- Attachment metadata: the images and files a ticket carries, whether uploaded in
-- a trau-native editor or discovered on a synced Linear/Jira issue. Bytes live on
-- disk under <trau home>/attachments, content-addressed by sha256; this table is
-- the index over them. Rows key on (repo, issue_identifier) TEXT rather than an
-- issues foreign key because sync wholesale-replaces a repo's synced issues, which
-- would take an id-keyed row down with it; an empty issue_identifier is an upload
-- not yet bound to an issue. source is one of upload, linear, jira, external, and
-- state one of pending, cached, failed: a tracker-hosted file is registered pending
-- with its source_url and downloaded lazily on first view, then cached with its
-- sha256/size/mime, or failed with the reason. source_url is empty for uploads,
-- which have no upstream copy. Two rows may share one sha256, so deleting a row
-- removes its file only once no other row references it.
CREATE TABLE attachments (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    repo             TEXT NOT NULL,
    issue_identifier TEXT NOT NULL DEFAULT '',
    source           TEXT NOT NULL,
    source_url       TEXT NOT NULL DEFAULT '',
    filename         TEXT NOT NULL DEFAULT '',
    mime_type        TEXT NOT NULL DEFAULT '',
    size_bytes       INTEGER NOT NULL DEFAULT 0,
    sha256           TEXT NOT NULL DEFAULT '',
    state            TEXT NOT NULL DEFAULT 'pending',
    error            TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL DEFAULT '',
    fetched_at       TEXT NOT NULL DEFAULT ''
) STRICT;

-- Re-syncs and repeated views of the same tracker file dedupe onto one row; uploads
-- carry no source_url and are exempt.
CREATE UNIQUE INDEX attachments_repo_source_url ON attachments(repo, source_url) WHERE source_url <> '';

CREATE INDEX attachments_repo_issue ON attachments(repo, issue_identifier);
CREATE INDEX attachments_sha256 ON attachments(sha256);
