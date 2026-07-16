-- Index assignee_name alongside the existing issue text (ADR 0014) so searching a
-- person's name finds their issues. FTS5 content tables cannot ALTER, so the
-- virtual table and its triggers are dropped and recreated with the new column,
-- then rebuilt from issues — repopulating rows synced before this migration.
DROP TRIGGER issues_fts_ai;
DROP TRIGGER issues_fts_ad;
DROP TRIGGER issues_fts_au;
DROP TABLE issues_fts;

CREATE VIRTUAL TABLE issues_fts USING fts5(
    identifier,
    title,
    description,
    labels,
    assignee_name,
    content = 'issues',
    content_rowid = 'id',
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER issues_fts_ai AFTER INSERT ON issues BEGIN
    INSERT INTO issues_fts(rowid, identifier, title, description, labels, assignee_name)
    VALUES (new.id, new.identifier, new.title, new.description, new.labels, new.assignee_name);
END;

CREATE TRIGGER issues_fts_ad AFTER DELETE ON issues BEGIN
    INSERT INTO issues_fts(issues_fts, rowid, identifier, title, description, labels, assignee_name)
    VALUES ('delete', old.id, old.identifier, old.title, old.description, old.labels, old.assignee_name);
END;

CREATE TRIGGER issues_fts_au AFTER UPDATE ON issues BEGIN
    INSERT INTO issues_fts(issues_fts, rowid, identifier, title, description, labels, assignee_name)
    VALUES ('delete', old.id, old.identifier, old.title, old.description, old.labels, old.assignee_name);
    INSERT INTO issues_fts(rowid, identifier, title, description, labels, assignee_name)
    VALUES (new.id, new.identifier, new.title, new.description, new.labels, new.assignee_name);
END;

INSERT INTO issues_fts(issues_fts) VALUES ('rebuild');
