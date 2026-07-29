-- The board descends the parent chain twice — from an archived row to hide its
-- whole subtree, and from a parent to promote it while work below it is started —
-- joining on (repo, parent) once per level.
CREATE INDEX issues_repo_parent ON issues(repo, parent);
