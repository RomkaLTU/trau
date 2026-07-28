-- Whether a grilling session answers its own recommendation-bearing questions: an
-- ask_user call carrying a recommended option is answered with it at once and only a
-- question without one reaches the user. Locked at create like the mode it runs in; 0
-- keeps every legacy and in-flight session on the ask-everything default.
ALTER TABLE grill_sessions ADD COLUMN auto_accept INTEGER NOT NULL DEFAULT 0;
