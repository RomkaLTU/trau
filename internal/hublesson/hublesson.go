// Package hublesson is the loop child's hub-backed store for the per-repo lessons
// ledger — the distilled repair-experiment takeaways a failed or repaired run leaves
// for later runs (COD-529, ADR 0008). The child posts each distilled lesson to the
// serve hub over HTTP as verify records it and recalls the recorded ones for
// build/verify/repair prompt injection from there; it never reads or writes a ledger
// file. Reads and writes retry a briefly unreachable hub before returning
// state.ErrHubUnreachable, matching the checkpoint store's contract.
package hublesson

import (
	"context"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/state"
)

// defaultRetryWindow is the total time a call is retried before it gives up on an
// unreachable hub (ADR 0008 §3, config HUB_WRITE_RETRY_WINDOW).
const defaultRetryWindow = 30 * time.Second

const (
	baseBackoff = 200 * time.Millisecond
	maxBackoff  = 2 * time.Second
)

// hubAPI is the slice of hubclient the lessons store drives; *hubclient.Client
// satisfies it, and tests substitute a fake.
type hubAPI interface {
	AppendLesson(ctx context.Context, repo string, l hubclient.Lesson) error
	Lessons(ctx context.Context, repo string) ([]hubclient.Lesson, error)
}

// Store is a hub-backed lessons ledger scoped to one repo. isUnreachable, set from
// hubclient.IsUnreachable at construction, distinguishes a hub-connection failure
// (retryable) from an error the hub returned.
type Store struct {
	client        hubAPI
	repo          string
	retryWindow   time.Duration
	isUnreachable func(error) bool

	now   func() time.Time
	sleep func(time.Duration)
}

// New returns a Store driving repo's lessons through client, retrying an unreachable
// hub (per isUnreachable) for retryWindow before giving up.
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

// Append records one distilled lesson for the repo through the hub, returning
// state.ErrHubUnreachable when the hub stays down past the retry window.
func (s *Store) Append(l hubclient.Lesson) error {
	return s.retry(func(ctx context.Context) error {
		return s.client.AppendLesson(ctx, s.repo, l)
	})
}

// All returns the repo's recorded lessons in append order — oldest first — so the
// pipeline's relevance scan selects and tie-breaks exactly as it did against the
// file-era ledger. The hub returns them most-recent first, so they are reversed here.
func (s *Store) All() ([]hubclient.Lesson, error) {
	var lessons []hubclient.Lesson
	err := s.retry(func(ctx context.Context) error {
		var e error
		lessons, e = s.client.Lessons(ctx, s.repo)
		return e
	})
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(lessons)-1; i < j; i, j = i+1, j-1 {
		lessons[i], lessons[j] = lessons[j], lessons[i]
	}
	return lessons, nil
}

// retry runs fn until it succeeds, returns a non-connection error, or the retry
// window expires, backing off exponentially between attempts. Exhaustion against an
// unreachable hub returns state.ErrHubUnreachable.
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
