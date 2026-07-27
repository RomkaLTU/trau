-- Web Push delivery state, hub-global rather than per-repo: the notification is
-- an OS-level wake-up for this machine, not a fact about one repo. push_vapid
-- holds the single application identity the hub signs every push with,
-- generated on first need; browsers bind a subscription to the public key they
-- subscribed under, so the row is a singleton and never rotates behind them.
-- push_subscriptions holds one row per browser endpoint, keyed by the endpoint
-- URL the push service issued — re-subscribing the same browser refreshes its
-- keys in place. Rows are pruned when a push service reports the endpoint gone.
CREATE TABLE push_vapid (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    public_key  TEXT NOT NULL,
    private_key TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE push_subscriptions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    endpoint   TEXT NOT NULL UNIQUE,
    p256dh     TEXT NOT NULL,
    auth       TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT ''
) STRICT;
