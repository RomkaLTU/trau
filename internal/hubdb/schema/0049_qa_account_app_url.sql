-- Which app URL entry a QA account signs into. An account attached to an entry
-- is offered only to the verify driving that entry's URL; one left null applies
-- to every URL. ON DELETE SET NULL so removing an app URL detaches its accounts
-- instead of taking their credentials down with it. Additive and forward-only:
-- an account stored before this migration reads as unattached.
ALTER TABLE qa_accounts ADD COLUMN app_url_id INTEGER REFERENCES app_urls(id) ON DELETE SET NULL;

CREATE INDEX qa_accounts_app_url ON qa_accounts(app_url_id);
