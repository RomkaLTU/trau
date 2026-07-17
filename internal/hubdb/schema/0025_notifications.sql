-- The hub's durable notification record: the two needs-attention sources — a
-- grilling session waiting on the user, and a run that paused, faulted, or was
-- quarantined — persisted here so the web can surface a lasting "needs you" feed
-- and mark items read. kind is one of grill_question, run_paused, run_faulted,
-- run_quarantined; ref is the grilling session id or the run's ticket; issue_id
-- names the tracker issue when there is one. A grill_question coalesces on its
-- unread (kind, ref) row so one session never stacks more than one unread entry;
-- the run kinds are distinct facts and never coalesce. read_at is null while the
-- notification is unread. Retention prunes read rows beyond the newest 200 on
-- insert; unread rows are never pruned.
CREATE TABLE notifications (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    repo       TEXT NOT NULL,
    kind       TEXT NOT NULL,
    ref        TEXT NOT NULL,
    issue_id   TEXT,
    title      TEXT NOT NULL,
    body       TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT '',
    read_at    TEXT
) STRICT;

CREATE INDEX notifications_read_at ON notifications(read_at);
CREATE INDEX notifications_kind_ref ON notifications(kind, ref, read_at);
