-- Project-level tracker: the config keys a project resolves once and every
-- member repo shares. They are seeded into each member's .trau.ini so a CLI run
-- straight against a repo keeps reading its own config layers.
CREATE TABLE project_tracker (
    project TEXT NOT NULL,
    key     TEXT NOT NULL,
    value   TEXT NOT NULL,
    PRIMARY KEY (project, key)
) STRICT;

-- The keys a member root holds on its project's behalf rather than its own. A
-- project edit refreshes exactly these; anything else in the repo's file is an
-- explicit per-repo override seeding must not touch.
CREATE TABLE project_tracker_seeded (
    root TEXT NOT NULL,
    key  TEXT NOT NULL,
    PRIMARY KEY (root, key)
) STRICT;
