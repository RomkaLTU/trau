-- Logical projects: the named groups that organize registered repo roots. The
-- registration tables stay the source of truth for which repos exist; a project
-- only references their roots. A root is a primary key here, which is what makes
-- a repo belong to at most one project.
CREATE TABLE projects (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE project_repos (
    root     TEXT PRIMARY KEY,
    project  TEXT NOT NULL,
    position INTEGER NOT NULL
) STRICT;

CREATE INDEX project_repos_project ON project_repos(project, position);
