-- Per-ticket queue of operator steer notes: free-text messages typed at a
-- running ticket that the pipeline hands to whichever agent works next, without
-- stopping the loop. A note starts pending and leaves the queue exactly once —
-- delivered, stamped with the canonical phase label of the agent that consumed
-- it, or expired when the ticket's run settles with the note still undelivered.
-- Ordering is by id, so a ticket's notes are delivered oldest-first and the UI
-- timeline reads them in the order they were typed.
CREATE TABLE steer_notes (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    repo            TEXT NOT NULL,
    ticket          TEXT NOT NULL,
    body            TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    delivered_phase TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL DEFAULT '',
    delivered_at    TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX steer_notes_ticket ON steer_notes(repo, ticket, id);
