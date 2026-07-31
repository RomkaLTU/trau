package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/tracker"
)

// blockingReader holds SyncPull open until the test releases it, so another
// request lands while a pull is genuinely in flight.
type blockingReader struct {
	*fakeReader
	entered chan struct{}
	release chan struct{}
}

func newBlockingReader() *blockingReader {
	return &blockingReader{
		fakeReader: &fakeReader{synced: syncedFixture()},
		entered:    make(chan struct{}, 1),
		release:    make(chan struct{}),
	}
}

func (b *blockingReader) SyncPull(ctx context.Context, binding tracker.ProjectBinding, since string) ([]tracker.SyncedIssue, error) {
	b.entered <- struct{}{}
	<-b.release
	return b.fakeReader.SyncPull(ctx, binding, since)
}

// postAsync fires a POST without blocking the test, handing back the status the
// hub answered with once the request completes.
func postAsync(ts *httptest.Server, path string) <-chan int {
	done := make(chan int, 1)
	go func() {
		res, err := http.Post(ts.URL+APIPrefix+path, "application/json", nil)
		if err != nil {
			done <- 0
			return
		}
		_ = res.Body.Close()
		done <- res.StatusCode
	}()
	return done
}

func acceptedBody(t *testing.T, res *http.Response) SyncAccepted {
	t.Helper()
	var out SyncAccepted
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode accepted body: %v", err)
	}
	return out
}

// A sync pressed while one is already running joins it: the second request is
// accepted without a pull of its own, the board read behind it does not start one
// either, and health reports the manual pull as syncing throughout.
func TestSyncCoalescesWithAnInFlightPull(t *testing.T) {
	reader := newBlockingReader()
	s, ts, _, _ := backlogServer(t, reader, nil)
	s.syncer.ctx, s.syncer.interval = context.Background(), time.Minute

	first := postAsync(ts, "/repos/acme/sync")
	<-reader.entered

	if _, health := getHealth(t, ts, "acme"); health.State != HealthSyncing {
		t.Fatalf("health state = %q, want %q during a manual pull", health.State, HealthSyncing)
	}

	res, _ := postSync(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 for a sync of a repo already syncing", res.StatusCode)
	}
	if body := acceptedBody(t, res); body.State != HealthSyncing || body.Repo != "acme" {
		t.Fatalf("body = %+v, want acme marked syncing", body)
	}

	boardRes, _ := getBacklog(t, ts, "acme")
	_ = boardRes.Body.Close()

	close(reader.release)
	if status := <-first; status != http.StatusOK {
		t.Fatalf("first sync status = %d, want 200", status)
	}
	if reader.syncCalls != 1 {
		t.Fatalf("SyncPull called %d times, want one pull across the second request and the board's stale refresh", reader.syncCalls)
	}
}

func TestForceResyncCoalescesWithAnInFlightPull(t *testing.T) {
	reader := newBlockingReader()
	ts, _, _ := syncServer(t, reader)

	first := postAsync(ts, "/repos/acme/sync")
	<-reader.entered

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/resync", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 for a resync of a repo already syncing", res.StatusCode)
	}
	if body := acceptedBody(t, res); body.State != HealthSyncing {
		t.Fatalf("body = %+v, want the syncing marker", body)
	}

	close(reader.release)
	if status := <-first; status != http.StatusOK {
		t.Fatalf("first sync status = %d, want 200", status)
	}
	if reader.syncCalls != 1 {
		t.Fatalf("SyncPull called %d times, want the resync to have coalesced", reader.syncCalls)
	}
}

// The manual claim is the same single-flight guard the loop uses, so the periodic
// tick and the stale-on-view refresh skip a repo a user is syncing — and a manual
// sync of a backed-off repo still runs, because the user is asking for it now.
func TestManualClaimRefusesOnlyWhileInFlight(t *testing.T) {
	sy := newSyncer(nil)
	root := "/repo/acme"

	if !sy.claimManual(root) {
		t.Fatal("the first manual claim should succeed")
	}
	if sy.claimManual(root) {
		t.Fatal("a second manual claim should coalesce while a sync is in flight")
	}
	if sy.claim(root, time.Now()) {
		t.Fatal("a background claim should skip a repo mid-manual-sync")
	}
	if !sy.syncing(root) {
		t.Fatal("health should report a manual pull as syncing")
	}

	sy.settleManual(root, errors.New("boom"))
	if sy.syncing(root) {
		t.Fatal("the repo should stop reporting syncing once the manual sync settles")
	}
	if sy.claim(root, time.Now()) {
		t.Fatal("the background loop should honour the backoff a failed manual sync left")
	}
	if !sy.claimManual(root) {
		t.Fatal("a manual sync should run even while the repo is backed off")
	}

	sy.settleManual(root, nil)
	if st := sy.state[root]; st.failures != 0 || !st.nextAttempt.IsZero() {
		t.Fatalf("state = %+v, want a successful manual sync to clear the backoff", st)
	}
}

// The claim bookkeeping is the endpoint's, not the loop's: with the background
// sync disabled there is no ctx or interval, and coalescing must still hold.
func TestManualClaimHoldsWithTheBackgroundLoopDisabled(t *testing.T) {
	sy := newSyncer(nil)
	sy.run(context.Background(), 0, 0)
	root := "/repo/acme"

	if !sy.claimManual(root) {
		t.Fatal("a manual claim should succeed with the loop disabled")
	}
	if sy.claimManual(root) {
		t.Fatal("a manual claim should still coalesce with the loop disabled")
	}
	sy.settleManual(root, nil)
	if !sy.claimManual(root) {
		t.Fatal("a settled manual sync should free the repo for the next one")
	}
}
