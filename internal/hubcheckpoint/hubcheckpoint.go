// Package hubcheckpoint is the loop child's hub-backed checkpoint store. It
// satisfies state.Checkpoints by driving the serve hub's checkpoint API over
// HTTP (ADR 0008): the child writes every phase transition to the hub and never
// opens a database. Writes buffer the ticket's fields in memory and flush with a
// bounded retry; when the hub stays unreachable past the retry window a write
// returns state.ErrHubUnreachable, which the pipeline turns into a blameless pause.
package hubcheckpoint

import (
	"context"
	"maps"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/sanitize"
	"github.com/RomkaLTU/trau/internal/state"
)

// defaultRetryWindow is the total time a write is retried before it pauses the
// run (ADR 0008 §3, config HUB_WRITE_RETRY_WINDOW).
const defaultRetryWindow = 30 * time.Second

const (
	baseBackoff = 200 * time.Millisecond
	maxBackoff  = 2 * time.Second
)

// hubAPI is the slice of hubclient the checkpoint store drives; *hubclient.Client
// satisfies it, and tests substitute a fake.
type hubAPI interface {
	PutCheckpoint(ctx context.Context, repo, ticket string, cp hubclient.Checkpoint) error
	GetCheckpoint(ctx context.Context, repo, ticket string) (hubclient.Checkpoint, bool, error)
	DeleteCheckpoint(ctx context.Context, repo, ticket string) error
	Checkpoints(ctx context.Context, repo string) ([]hubclient.Checkpoint, error)
}

// Store is a hub-backed checkpoint store scoped to one repo. It caches each
// ticket's field set in memory, hydrated once from the hub, so reads are local
// and only writes cross the wire.
type Store struct {
	client      hubAPI
	repo        string
	retryWindow time.Duration

	now   func() time.Time
	sleep func(time.Duration)

	mu       sync.Mutex
	cache    map[string]map[string]string
	hydrated map[string]bool
}

// New returns a Store writing repo's checkpoints through client, retrying an
// unreachable hub for retryWindow (defaulted when non-positive) before pausing.
func New(client hubAPI, repo string, retryWindow time.Duration) *Store {
	if retryWindow <= 0 {
		retryWindow = defaultRetryWindow
	}
	return &Store{
		client:      client,
		repo:        repo,
		retryWindow: retryWindow,
		now:         time.Now,
		sleep:       time.Sleep,
		cache:       map[string]map[string]string{},
		hydrated:    map[string]bool{},
	}
}

// Get returns a checkpoint field for a ticket, hydrating the ticket from the hub
// on first touch. An unreachable hub yields "" rather than an error, since Get
// cannot signal a pause; the following write is what pauses the run.
func (s *Store) Get(id, key string) string {
	if err := s.hydrate(id); err != nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cache[id][key]
}

// Set upserts key=value and refreshes UPDATED, then flushes the ticket's whole
// field set to the hub. It hydrates first so a write never clobbers the hub's
// checkpoint with a partial set, and returns state.ErrHubUnreachable when the
// hub stays down past the retry window.
func (s *Store) Set(id, key, value string) error {
	if err := s.hydrate(id); err != nil {
		return err
	}
	return s.flush(id, func(fields map[string]string) {
		fields[key] = sanitize.StateValue(value)
	})
}

// Unset removes key and refreshes UPDATED, then flushes. A missing key still
// refreshes and flushes, matching the file store's last-write-wins semantics.
func (s *Store) Unset(id, key string) error {
	if err := s.hydrate(id); err != nil {
		return err
	}
	return s.flush(id, func(fields map[string]string) {
		delete(fields, key)
	})
}

// RemoveState drops the ticket's checkpoint from the hub and forgets its cache.
func (s *Store) RemoveState(id string) error {
	s.mu.Lock()
	delete(s.cache, id)
	delete(s.hydrated, id)
	s.mu.Unlock()
	return s.retry(func(ctx context.Context) error {
		return s.client.DeleteCheckpoint(ctx, s.repo, id)
	})
}

// Tickets returns every ticket the hub holds a checkpoint for in this repo.
func (s *Store) Tickets() []string {
	cps, err := s.list()
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(cps))
	for _, cp := range cps {
		ids = append(ids, cp.Ticket)
	}
	return ids
}

// ResumeTargetFunc returns the lowest-numbered in-flight checkpoint the keep
// predicate accepts, read from the hub. An unreachable hub yields ("", "").
func (s *Store) ResumeTargetFunc(keep func(id string) bool) (id, phase string) {
	cps, err := s.list()
	if err != nil {
		return "", ""
	}
	phases := make(map[string]string, len(cps))
	for _, cp := range cps {
		phases[cp.Ticket] = cp.Phase
	}
	return state.PickResumeTarget(phases, keep)
}

func (s *Store) hydrate(id string) error {
	s.mu.Lock()
	done := s.hydrated[id]
	s.mu.Unlock()
	if done {
		return nil
	}
	var (
		cp    hubclient.Checkpoint
		found bool
	)
	if err := s.retry(func(ctx context.Context) error {
		var e error
		cp, found, e = s.client.GetCheckpoint(ctx, s.repo, id)
		return e
	}); err != nil {
		return err
	}
	fields := map[string]string{}
	if found {
		maps.Copy(fields, cp.Data)
	}
	s.mu.Lock()
	s.cache[id] = fields
	s.hydrated[id] = true
	s.mu.Unlock()
	return nil
}

func (s *Store) flush(id string, mutate func(fields map[string]string)) error {
	s.mu.Lock()
	fields := s.cache[id]
	if fields == nil {
		fields = map[string]string{}
		s.cache[id] = fields
	}
	mutate(fields)
	fields["UPDATED"] = s.now().Format("2006-01-02 15:04:05")
	snapshot := maps.Clone(fields)
	s.mu.Unlock()

	return s.retry(func(ctx context.Context) error {
		return s.client.PutCheckpoint(ctx, s.repo, id, hubclient.Checkpoint{Ticket: id, Data: snapshot})
	})
}

func (s *Store) list() ([]hubclient.Checkpoint, error) {
	var cps []hubclient.Checkpoint
	err := s.retry(func(ctx context.Context) error {
		var e error
		cps, e = s.client.Checkpoints(ctx, s.repo)
		return e
	})
	return cps, err
}

// retry runs fn until it succeeds, returns a non-connection error, or the retry
// window expires, backing off exponentially between attempts. Exhaustion against
// an unreachable hub returns state.ErrHubUnreachable.
func (s *Store) retry(fn func(context.Context) error) error {
	deadline := s.now().Add(s.retryWindow)
	backoff := baseBackoff
	for {
		err := fn(context.Background())
		if err == nil {
			return nil
		}
		if !hubclient.IsUnreachable(err) {
			return err
		}
		if !s.now().Before(deadline) {
			return state.ErrHubUnreachable
		}
		s.sleep(backoff)
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}
