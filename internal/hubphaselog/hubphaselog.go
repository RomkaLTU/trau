// Package hubphaselog is the loop child's hub-backed store for the per-phase agent
// logs the TUI log inspector browses (ADR 0008). The child posts each phase's
// final output to the serve hub over HTTP as the phase produces it, and the
// inspector reads them back from there; it never lists or reads the run
// directory. Writes retry a briefly unreachable hub before returning
// state.ErrHubUnreachable, matching the checkpoint store's contract.
package hubphaselog

import (
	"context"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/state"
)

// defaultRetryWindow is the total time a write is retried before it gives up on an
// unreachable hub (ADR 0008 §3, config HUB_WRITE_RETRY_WINDOW).
const defaultRetryWindow = 30 * time.Second

const (
	baseBackoff = 200 * time.Millisecond
	maxBackoff  = 2 * time.Second
)

// PhaseLog is one phase's stored output, as the inspector reads it back,
// most-recently-written first.
type PhaseLog struct {
	Phase   string
	Content string
	Updated time.Time
}

// hubAPI is the slice of hubclient the phase-log store drives; *hubclient.Client
// satisfies it, and tests substitute a fake.
type hubAPI interface {
	PutPhaseLog(ctx context.Context, repo, ticket, phase, content string) error
	PhaseLogs(ctx context.Context, repo, ticket string) ([]hubclient.PhaseLog, error)
	DeletePhaseLogs(ctx context.Context, repo, ticket string) error
}

// Store is a hub-backed phase-log store scoped to one repo. isUnreachable, set
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

// New returns a Store driving repo's phase logs through client, retrying an
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

// Put stores a ticket's log for a phase through the hub, returning
// state.ErrHubUnreachable when the hub stays down past the retry window.
func (s *Store) Put(id, phase, content string) error {
	return s.retry(func(ctx context.Context) error {
		return s.client.PutPhaseLog(ctx, s.repo, id, phase, content)
	})
}

// List returns a ticket's stored phase logs, most-recently-written first.
func (s *Store) List(id string) ([]PhaseLog, error) {
	var logs []hubclient.PhaseLog
	err := s.retry(func(ctx context.Context) error {
		var e error
		logs, e = s.client.PhaseLogs(ctx, s.repo, id)
		return e
	})
	if err != nil {
		return nil, err
	}
	out := make([]PhaseLog, len(logs))
	for i, l := range logs {
		out[i] = PhaseLog{Phase: l.Phase, Content: l.Content, Updated: l.Updated}
	}
	return out, nil
}

// Remove drops every phase log the hub holds for a ticket.
func (s *Store) Remove(id string) error {
	return s.retry(func(ctx context.Context) error {
		return s.client.DeletePhaseLogs(ctx, s.repo, id)
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
