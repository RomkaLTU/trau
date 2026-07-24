-- When the current drain was armed, so the Loop page can time the loop run in
-- flight instead of every ticket the queue has ever carried. Set on the
-- transition into draining and cleared on the way out, so a queue that is not
-- draining carries no stamp. Additive and forward-only: a queue armed before
-- this migration reads empty until it is re-armed.
ALTER TABLE queue_repos ADD COLUMN draining_since TEXT NOT NULL DEFAULT '';
