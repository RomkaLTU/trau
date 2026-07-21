-- Per-repo QA credentials the browser verifier signs into an auth-walled app
-- with. qa_accounts holds one login per row: a human label, the username and
-- secret, and a description of the cases or flows the account covers so the
-- verifier can pick the right one for what is under test. The secret is
-- stored whole; the settings API masks it on read (write-only, ADR 0011)
-- while the loop's fetch path reads it in full (the hub is localhost-only, so
-- the machine-trust posture applies). qa_notes carries one free-text value
-- per repo for anything unstructured — login-path quirks, disposable-user
-- recipes, cleanup rules.
CREATE TABLE qa_accounts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    repo        TEXT NOT NULL,
    label       TEXT NOT NULL,
    username    TEXT NOT NULL DEFAULT '',
    secret      TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT '',
    updated_at  TEXT NOT NULL DEFAULT '',
    UNIQUE (repo, label)
) STRICT;

CREATE INDEX qa_accounts_repo ON qa_accounts(repo);

CREATE TABLE qa_notes (
    repo       TEXT PRIMARY KEY,
    notes      TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT ''
) STRICT;
