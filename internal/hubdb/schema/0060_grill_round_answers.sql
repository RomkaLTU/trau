-- A round's per-question answers (COD-1511). ask_round poses several independent
-- questions in one exchange, so what a session waits for is no longer one message but
-- a set: the answers already given must outlive the turn that collected them, or a
-- session parked mid-round would ask everything again when it resumes. The round's
-- questions live in the question message's payload; a row here answers one of them by
-- its position in that list, and auto marks an answer the hub took from the agent's
-- own recommendation rather than from the user.
CREATE TABLE grill_round_answers (
    message_id INTEGER NOT NULL REFERENCES grill_messages(id) ON DELETE CASCADE,
    idx        INTEGER NOT NULL,
    text       TEXT NOT NULL DEFAULT '',
    auto       INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (message_id, idx)
) STRICT;
