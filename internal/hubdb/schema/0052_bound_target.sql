-- The config target a repo's cached tracker binding was resolved from — the
-- settled provider plus the team/project keys, joined by \x1f (see webserver's
-- bindingTarget). A sync compares it against the repo's current config so a
-- .trau.ini retarget re-resolves the binding instead of being shadowed forever
-- by the resolved-id cache. Empty on rows stamped before this column existed;
-- the first sync after upgrade re-resolves once and backfills it.
ALTER TABLE issue_sync ADD COLUMN bound_target TEXT NOT NULL DEFAULT '';
