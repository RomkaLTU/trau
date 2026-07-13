-- Present-tense Activity on instance presence (ADR 0009): what pipeline work a
-- Working loop is doing right now — build, verify, repair, ci-wait, merge, … —
-- plus a free-text detail (the raw call label, e.g. repair2). Additive and
-- forward-only: an older child that reports neither leaves both empty, and the
-- API omits them.
ALTER TABLE instances ADD COLUMN activity TEXT NOT NULL DEFAULT '';
ALTER TABLE instances ADD COLUMN detail TEXT NOT NULL DEFAULT '';
