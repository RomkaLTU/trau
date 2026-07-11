CREATE VIRTUAL TABLE issues_fts USING fts5(
    identifier,
    title,
    description,
    labels,
    content = 'issues',
    content_rowid = 'id',
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER issues_fts_ai AFTER INSERT ON issues BEGIN
    INSERT INTO issues_fts(rowid, identifier, title, description, labels)
    VALUES (new.id, new.identifier, new.title, new.description, new.labels);
END;

CREATE TRIGGER issues_fts_ad AFTER DELETE ON issues BEGIN
    INSERT INTO issues_fts(issues_fts, rowid, identifier, title, description, labels)
    VALUES ('delete', old.id, old.identifier, old.title, old.description, old.labels);
END;

CREATE TRIGGER issues_fts_au AFTER UPDATE ON issues BEGIN
    INSERT INTO issues_fts(issues_fts, rowid, identifier, title, description, labels)
    VALUES ('delete', old.id, old.identifier, old.title, old.description, old.labels);
    INSERT INTO issues_fts(rowid, identifier, title, description, labels)
    VALUES (new.id, new.identifier, new.title, new.description, new.labels);
END;

INSERT INTO issues_fts(issues_fts) VALUES ('rebuild');
