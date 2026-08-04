-- Set on the issues this hub put its QUEUED_LABEL on, cleared when the hub takes
-- the label off again. The post-sync reconcile strips a leftover queued label only
-- from rows it owns — this flag, or a queue row still naming the issue — so on a
-- tracker several hubs share it never fights a label another hub or a human wrote.
-- The flag is what outlives the queue row a dequeue or an archive deletes. Rows
-- labelled before this column existed carry 0 and lean on the queue's own history.
ALTER TABLE issues ADD COLUMN queued_label_placed INTEGER NOT NULL DEFAULT 0;
