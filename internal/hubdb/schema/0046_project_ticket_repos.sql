-- The member repo a ticket was started in, per project. A project with several
-- members asks which repo a run targets; this is what makes the answer stick, so
-- re-queueing or resuming the same ticket pre-selects the repo it ran in before,
-- and a ticket nothing matches falls back to the member most recently started.
CREATE TABLE project_ticket_repos (
    project    TEXT NOT NULL,
    ticket     TEXT NOT NULL,
    root       TEXT NOT NULL,
    started_at TEXT NOT NULL,
    PRIMARY KEY (project, ticket)
) STRICT;

CREATE INDEX project_ticket_repos_recent ON project_ticket_repos(project, started_at);
