-- Blocking relations a split-outcome apply has written to the tracker. A partial
-- apply that created a sub-issue but failed to link its blocked_by relation must
-- re-attempt only that relation on retry — never a relation that already landed,
-- since tracker link writes are not idempotent. Recording each wired relation
-- separates "sub-issue exists" from "relation wired" so retries settle both.
CREATE TABLE grill_relations (
    repo    TEXT NOT NULL,
    blocker TEXT NOT NULL,
    blocked TEXT NOT NULL,
    PRIMARY KEY (repo, blocker, blocked)
) STRICT;
