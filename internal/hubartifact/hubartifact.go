// Package hubartifact is the loop child's hub-backed store for the durable
// per-run phase artifacts — the handoff brief, verify rubric, verify verdict, and
// build notes (ADR 0008). The child posts each artifact to the serve hub over HTTP
// as its phase produces it and restores it from there on resume; it never opens a
// database or writes a run file. Writes retry a briefly unreachable hub before
// returning state.ErrHubUnreachable, matching the checkpoint store's contract.
package hubartifact

import (
	"context"
	"time"

	"github.com/RomkaLTU/trau/internal/state"
)

// defaultRetryWindow is the total time a write is retried before it gives up on an
// unreachable hub (ADR 0008 §3, config HUB_WRITE_RETRY_WINDOW).
const defaultRetryWindow = 30 * time.Second

const (
	baseBackoff = 200 * time.Millisecond
	maxBackoff  = 2 * time.Second
)

// hubAPI is the slice of hubclient the artifact store drives; *hubclient.Client
// satisfies it, and tests substitute a fake.
type hubAPI interface {
	PutArtifact(ctx context.Context, repo, ticket, kind, content string) error
	GetArtifact(ctx context.Context, repo, ticket, kind string) (string, bool, error)
	DeleteArtifacts(ctx context.Context, repo, ticket string) error
}

// Store is a hub-backed artifact store scoped to one repo. isUnreachable, set
// from hubclient.IsUnreachable at construction, distinguishes a hub-connection
// failure (retryable) from an error the hub returned.
type Store struct {
	client        hubAPI
	repo          string
	retryWindow   time.Duration
	isUnreachable func(error) bool

	now   func() time.Time
	sleep func(time.Duration)
}

// New returns a Store writing repo's artifacts through client, retrying an
// unreachable hub (per isUnreachable) for retryWindow before giving up.
func New(client hubAPI, repo string, retryWindow time.Duration, isUnreachable func(error) bool) *Store {
	if retryWindow <= 0 {
		retryWindow = defaultRetryWindow
	}
	return &Store{
		client:        client,
		repo:          repo,
		retryWindow:   retryWindow,
		isUnreachable: isUnreachable,
		now:           time.Now,
		sleep:         time.Sleep,
	}
}

// Put writes a ticket's artifact of the given kind to the hub, returning
// state.ErrHubUnreachable when the hub stays down past the retry window.
func (s *Store) Put(id, kind, content string) error {
	return s.retry(func(ctx context.Context) error {
		return s.client.PutArtifact(ctx, s.repo, id, kind, content)
	})
}

// Get reads a ticket's artifact of the given kind from the hub, returning
// ok=false when the hub holds none.
func (s *Store) Get(id, kind string) (content string, ok bool, err error) {
	err = s.retry(func(ctx context.Context) error {
		var e error
		content, ok, e = s.client.GetArtifact(ctx, s.repo, id, kind)
		return e
	})
	if err != nil {
		return "", false, err
	}
	return content, ok, nil
}

// Remove drops every artifact the hub holds for a ticket.
func (s *Store) Remove(id string) error {
	return s.retry(func(ctx context.Context) error {
		return s.client.DeleteArtifacts(ctx, s.repo, id)
	})
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
		if !s.isUnreachable(err) {
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
