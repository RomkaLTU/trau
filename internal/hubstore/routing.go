package hubstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
)

// RoutingFingerprint is the routing configuration a repo ran under: the stable
// hash its token calls are stamped with, and the routing-relevant key/value pairs
// the hash was computed over.
type RoutingFingerprint struct {
	Hash string
	Keys map[string]string
	TS   string
}

// RoutingChange is one key whose value differs from the repo's last fingerprint.
// From is empty for a key the previous fingerprint did not carry, To for one the
// new fingerprint dropped.
type RoutingChange struct {
	Key  string
	From string
	To   string
}

// Routing is the hub's record of the routing fingerprint each repo last ran
// under, so a run whose configuration differs is recognizable as a cohort
// boundary rather than something to reconstruct from dates.
type Routing struct {
	db *sql.DB
}

// NewRouting returns a Routing store over db.
func NewRouting(db *sql.DB) *Routing {
	return &Routing{db: db}
}

// Observe records repo's current routing fingerprint and returns the keys whose
// values differ from the last one, sorted by key, alongside the hash they were
// compared against. A first observation reports every key as changed — the opening
// boundary of the repo's first cohort — and an identical fingerprint reports none
// and leaves the stored row untouched.
func (r *Routing) Observe(repo string, fp RoutingFingerprint) (changes []RoutingChange, prevHash string, err error) {
	prev, err := r.Last(repo)
	if err != nil {
		return nil, "", err
	}
	changes = diffRoutingKeys(prev.Keys, fp.Keys)
	if prev.Hash == fp.Hash && len(changes) == 0 {
		return nil, prev.Hash, nil
	}
	if _, err := r.db.Exec(
		`INSERT INTO repo_routing(repo, hash, keys, ts)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(repo) DO UPDATE SET hash = excluded.hash, keys = excluded.keys, ts = excluded.ts`,
		repo, fp.Hash, marshalRoutingKeys(fp.Keys), fp.TS,
	); err != nil {
		return nil, prev.Hash, err
	}
	return changes, prev.Hash, nil
}

// Last returns the fingerprint repo last ran under, zero-valued when the hub has
// never seen one for it.
func (r *Routing) Last(repo string) (RoutingFingerprint, error) {
	var (
		fp   RoutingFingerprint
		keys string
	)
	err := r.db.QueryRow(`SELECT hash, keys, ts FROM repo_routing WHERE repo = ?`, repo).Scan(&fp.Hash, &keys, &fp.TS)
	if errors.Is(err, sql.ErrNoRows) {
		return RoutingFingerprint{}, nil
	}
	if err != nil {
		return RoutingFingerprint{}, err
	}
	fp.Keys = unmarshalRoutingKeys(keys)
	return fp, nil
}

func diffRoutingKeys(prev, next map[string]string) []RoutingChange {
	names := make([]string, 0, len(prev)+len(next))
	seen := make(map[string]bool, len(prev)+len(next))
	for _, m := range []map[string]string{prev, next} {
		for name := range m {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)

	var changes []RoutingChange
	for _, name := range names {
		if prev[name] == next[name] {
			continue
		}
		changes = append(changes, RoutingChange{Key: name, From: prev[name], To: next[name]})
	}
	return changes
}

func marshalRoutingKeys(keys map[string]string) string {
	if len(keys) == 0 {
		return ""
	}
	b, err := json.Marshal(keys)
	if err != nil {
		return ""
	}
	return string(b)
}

func unmarshalRoutingKeys(s string) map[string]string {
	if s == "" {
		return nil
	}
	var out map[string]string
	if json.Unmarshal([]byte(s), &out) != nil {
		return nil
	}
	return out
}
