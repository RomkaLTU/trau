-- Prompt overrides: user-edited template bodies replacing the prompt registry's
-- built-in defaults. repo = '' is the global scope; otherwise the repo root,
-- keying repos the way the other per-repo tables do. Consumption resolves
-- repo > global > built-in default.
CREATE TABLE prompt_overrides (
    name       TEXT NOT NULL,
    repo       TEXT NOT NULL DEFAULT '',
    body       TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (name, repo)
) STRICT;
