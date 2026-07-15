package webserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/tracker"
)

func TestSyncBackoffGrowsAndCaps(t *testing.T) {
	interval := time.Minute
	if got := syncBackoff(interval, 1); got != 2*time.Minute {
		t.Fatalf("backoff(1) = %v, want 2m", got)
	}
	if got := syncBackoff(interval, 2); got != 4*time.Minute {
		t.Fatalf("backoff(2) = %v, want 4m", got)
	}
	if got := syncBackoff(interval, 100); got != syncBackoffCap {
		t.Fatalf("backoff(100) = %v, want the cap %v", got, syncBackoffCap)
	}
}

func TestSyncerClaimGuardsAndBacksOff(t *testing.T) {
	sy := newSyncer(nil)
	root := "/repo/acme"

	if !sy.claim(root, time.Now()) {
		t.Fatal("first claim should succeed")
	}
	if sy.claim(root, time.Now()) {
		t.Fatal("a second claim should fail while a sync is in flight")
	}
	if !sy.syncing(root) {
		t.Fatal("repo should report syncing after being claimed")
	}

	sy.settle(root, time.Second, nil)
	if sy.syncing(root) {
		t.Fatal("repo should not report syncing after it settles")
	}
	if !sy.claim(root, time.Now()) {
		t.Fatal("claim should succeed again after a successful settle")
	}

	sy.settle(root, time.Hour, errors.New("boom"))
	if sy.claim(root, time.Now()) {
		t.Fatal("claim should be blocked during the failure backoff")
	}
	if !sy.claim(root, time.Now().Add(syncBackoffCap+time.Minute)) {
		t.Fatal("claim should succeed once the backoff window passes")
	}
}

func TestSyncerReaderUnavailableBacksOffWithoutFailure(t *testing.T) {
	sy := newSyncer(nil)
	root := "/repo/nocreds"

	sy.claim(root, time.Now())
	sy.settle(root, time.Second, tracker.ErrReaderUnavailable)

	if sy.claim(root, time.Now()) {
		t.Fatal("a no-creds repo should back off before its next attempt")
	}
	if !sy.claim(root, time.Now().Add(syncBackoffCap+time.Minute)) {
		t.Fatal("a no-creds repo should be retried once the cap passes")
	}
}

func TestReconcileDueSchedulesAndBacksOff(t *testing.T) {
	sy := newSyncer(nil)
	sy.reconcileEvery = time.Hour
	root := "/repo/acme"
	sy.claim(root, time.Now())

	if !sy.reconcileDue(root) {
		t.Fatal("the first sweep should be due immediately")
	}
	sy.settleReconcile(root, nil)
	if sy.reconcileDue(root) {
		t.Fatal("a swept repo should not be due again until the interval elapses")
	}
	sy.settleReconcile(root, errors.New("boom"))
	if sy.reconcileDue(root) {
		t.Fatal("a failed sweep should back off before its next attempt")
	}
}

func TestReconcileDisabledWhenIntervalZero(t *testing.T) {
	sy := newSyncer(nil)
	root := "/repo/acme"
	sy.claim(root, time.Now())
	if sy.reconcileDue(root) {
		t.Fatal("reconcile must stay off when its interval is zero")
	}
}

func TestReposFreshnessSurfacesSyncState(t *testing.T) {
	fake := &fakeReader{synced: syncedFixture()}
	ts, _, _ := syncServer(t, fake)

	res, _ := postSync(t, ts, "acme")
	_ = res.Body.Close()

	rv := findRepoView(t, getRepos(t, ts), "acme")
	if rv.Freshness == nil {
		t.Fatal("freshness absent after a sync")
	}
	if rv.Freshness.LastSyncedAt == "" || rv.Freshness.LastIssues != 1 {
		t.Fatalf("freshness = %+v, want a synced time and one issue", rv.Freshness)
	}
	if rv.Freshness.LastError != "" || rv.Freshness.Syncing {
		t.Fatalf("freshness = %+v, want no error and not currently syncing", rv.Freshness)
	}
}

func TestReposFreshnessCarriesStateBeforeSync(t *testing.T) {
	ts, _, _ := syncServer(t, &fakeReader{})

	rv := findRepoView(t, getRepos(t, ts), "acme")
	if rv.Freshness == nil {
		t.Fatal("freshness absent; want a state even before any sync")
	}
	if rv.Freshness.State != HealthUnconfigured {
		t.Fatalf("state = %q, want unconfigured for a repo with no tracker config", rv.Freshness.State)
	}
	if rv.Freshness.LastSyncedAt != "" || rv.Freshness.Syncing {
		t.Fatalf("freshness = %+v, want no sync facts before any sync", rv.Freshness)
	}
}

func getRepos(t *testing.T, ts *httptest.Server) []RepoView {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos")
	if err != nil {
		t.Fatalf("GET repos: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var out ReposResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode repos: %v", err)
	}
	return out.Repos
}

func findRepoView(t *testing.T, views []RepoView, name string) RepoView {
	t.Helper()
	for _, rv := range views {
		if rv.Name == name {
			return rv
		}
	}
	t.Fatalf("repo %q not in %+v", name, views)
	return RepoView{}
}
