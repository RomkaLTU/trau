package hubstore

import (
	"database/sql"
	"errors"
	"time"
)

// VAPID is the hub's Web Push application identity. One keypair serves every
// subscription: a browser binds its subscription to the public key it subscribed
// under, so rotating the pair would silently orphan every existing endpoint.
type VAPID struct {
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"-"`
}

// PushSubscription is one browser's push endpoint and the keys its payloads are
// encrypted to, as PushSubscription.toJSON() reports them.
type PushSubscription struct {
	ID        int64  `json:"id"`
	Endpoint  string `json:"endpoint"`
	P256dh    string `json:"p256dh"`
	Auth      string `json:"auth"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Push is the hub's Web Push store: the machine-wide VAPID identity and the
// browser subscriptions a push is delivered to. The caller owns db's lifecycle.
type Push struct {
	db *sql.DB
}

// NewPush returns a Push store over db.
func NewPush(db *sql.DB) *Push { return &Push{db: db} }

const pushSubscriptionSelect = `SELECT id, endpoint, p256dh, auth, created_at, updated_at
	 FROM push_subscriptions`

// VAPID returns the stored identity, or the zero value when none has been
// generated yet.
func (p *Push) VAPID() (VAPID, error) {
	var v VAPID
	err := p.db.QueryRow(`SELECT public_key, private_key FROM push_vapid WHERE id = 1`).
		Scan(&v.PublicKey, &v.PrivateKey)
	if errors.Is(err, sql.ErrNoRows) {
		return VAPID{}, nil
	}
	if err != nil {
		return VAPID{}, err
	}
	return v, nil
}

// SaveVAPID persists v as the hub's identity and returns whatever is stored
// afterwards. An identity already present wins, so two callers racing to generate
// one still agree on the key browsers subscribed under.
func (p *Push) SaveVAPID(v VAPID) (VAPID, error) {
	if _, err := p.db.Exec(
		`INSERT INTO push_vapid(id, public_key, private_key, created_at) VALUES(1, ?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		v.PublicKey, v.PrivateKey, formatPushTime(time.Now()),
	); err != nil {
		return VAPID{}, err
	}
	return p.VAPID()
}

// Subscribe records a browser's subscription, refreshing the keys of one already
// stored under the same endpoint rather than stacking a second row.
func (p *Push) Subscribe(endpoint, p256dh, auth string) (PushSubscription, error) {
	now := formatPushTime(time.Now())
	if _, err := p.db.Exec(
		`INSERT INTO push_subscriptions(endpoint, p256dh, auth, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(endpoint) DO UPDATE SET
			p256dh = excluded.p256dh, auth = excluded.auth, updated_at = excluded.updated_at`,
		endpoint, p256dh, auth, now, now,
	); err != nil {
		return PushSubscription{}, err
	}
	subs, err := p.scan(pushSubscriptionSelect+` WHERE endpoint = ?`, endpoint)
	if err != nil {
		return PushSubscription{}, err
	}
	if len(subs) == 0 {
		return PushSubscription{}, sql.ErrNoRows
	}
	return subs[0], nil
}

// Unsubscribe drops the subscription at endpoint. Dropping an unknown endpoint is
// a no-op, so a browser that already forgot its subscription still unsubscribes
// cleanly.
func (p *Push) Unsubscribe(endpoint string) error {
	_, err := p.db.Exec(`DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	return err
}

// Subscriptions returns every stored subscription, oldest first.
func (p *Push) Subscriptions() ([]PushSubscription, error) {
	return p.scan(pushSubscriptionSelect + ` ORDER BY id`)
}

func (p *Push) scan(query string, args ...any) (out []PushSubscription, err error) {
	q, err := p.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, q.Close()) }()
	out = []PushSubscription{}
	for q.Next() {
		var sub PushSubscription
		if err := q.Scan(
			&sub.ID, &sub.Endpoint, &sub.P256dh, &sub.Auth, &sub.CreatedAt, &sub.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, q.Err()
}

func formatPushTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
