package hubstore

import "database/sql"

// Stores is the hub's set of SQLite-backed stores, all over the one database the
// serve process opens. It hands out the registration store and per-repo queue
// stores so the web server depends on a single hub-owned object rather than the
// raw database handle. The caller owns the database's lifecycle.
type Stores struct {
	db    *sql.DB
	repos *Registrations
}

// NewStores builds the hub store set over db.
func NewStores(db *sql.DB) *Stores {
	return &Stores{db: db, repos: NewRegistrations(db)}
}

// Registrations returns the registration store.
func (s *Stores) Registrations() *Registrations { return s.repos }

// Queue returns the queue store for a repo root.
func (s *Stores) Queue(root string) *Queue { return NewQueue(s.db, root) }

// ImportLegacyQueues imports the file-era queue.json of every repo the hub
// already tracks — known and web-registered — into the queue tables, removing
// each file after its rows commit. A failed import returns an error naming the
// file and leaves it in place, so serve startup can abort without losing a
// queue. Repos registered later import their queue.json lazily on first touch.
func (s *Stores) ImportLegacyQueues() error {
	roots, err := s.queueRoots()
	if err != nil {
		return err
	}
	for _, root := range roots {
		if err := s.Queue(root).ImportLegacy(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Stores) queueRoots() ([]string, error) {
	known, err := s.repos.Known()
	if err != nil {
		return nil, err
	}
	registered, err := s.repos.Registered()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(known)+len(registered))
	roots := make([]string, 0, len(known)+len(registered))
	add := func(root string) {
		if root == "" {
			return
		}
		if _, ok := seen[root]; ok {
			return
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	for _, repo := range known {
		add(repo.Root)
	}
	for _, root := range registered {
		add(root)
	}
	return roots, nil
}
