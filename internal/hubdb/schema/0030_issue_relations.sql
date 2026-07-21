-- The blocked-by graph between stored issues, one row per blocker→blocked edge.
-- Edges key on human identifiers the way wireGrillBlocks and the tracker readers
-- speak — never on issues rowids, which sync replaces wholesale. Internal epics
-- write edges on grill apply; inbound sync reflects the links an external tracker
-- reports. An edge may reference a blocker not (yet) in issues — a slice created
-- out of order, or a blocker outside the synced Project — and is kept as a
-- dangling edge that eligibility counts as unresolved, rather than being dropped.
CREATE TABLE issue_relations (
    repo    TEXT NOT NULL,
    blocker TEXT NOT NULL,
    blocked TEXT NOT NULL,
    PRIMARY KEY (repo, blocker, blocked)
) STRICT;

CREATE INDEX issue_relations_blocked ON issue_relations(repo, blocked);
