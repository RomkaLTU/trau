-- The Provider pinned on an issue, empty when its runs use the repo default.
-- Hub-local metadata: no sync write touches the column, so a pull never clears a pin.
ALTER TABLE issues ADD COLUMN provider TEXT NOT NULL DEFAULT '';
