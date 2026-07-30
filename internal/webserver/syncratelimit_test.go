package webserver

import (
	"bytes"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/tracker"
	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
)

func rateLimitErr(reset time.Time) error {
	return &linearapi.RateLimitError{
		Message: "Rate limit exceeded. Only 2500 requests are allowed per 1 hour.",
		ResetAt: reset,
	}
}

// A spent request budget is not a broken repo: recording it would pin health at
// sync-failed until something else succeeded, with nothing for the user to fix.
func TestSyncRateLimitDoesNotRecordFailure(t *testing.T) {
	fake := &fakeReader{synced: syncedFixture()}
	ts, root, store := syncServer(t, fake)

	res, _ := postSync(t, ts, "acme")
	_ = res.Body.Close()

	fake.syncErr = rateLimitErr(time.Now().Add(20 * time.Minute))
	res, _ = postSync(t, ts, "acme")
	_ = res.Body.Close()

	st, err := store.SyncState(root)
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if st.LastError != "" {
		t.Fatalf("last error = %q, want a rate limit left off the repo's health", st.LastError)
	}
	if st.LastSyncedAt == "" || st.LastIssues != 1 {
		t.Fatalf("state = %+v, want the last good sync preserved", st)
	}
}

// A repo whose provider was inferred has its pull failures wrapped in the
// provider hint before they reach the health surface and the sync worker. The
// wrap must stay transparent: a rate limit that reads as a plain failure pins the
// repo at sync-failed with exponential backoff.
func TestInferredProviderHintKeepsErrorClassification(t *testing.T) {
	res := trackerResolution{provider: "linear", jiraCreds: true}

	limitErr := res.actionableErr(rateLimitErr(time.Now().Add(10 * time.Minute)))
	if _, limited := tracker.RateLimited(limitErr); !limited {
		t.Fatalf("err = %v, want a rate limit still classified through the hint", limitErr)
	}
	if authErr := res.actionableErr(linearapi.ErrUnauthorized); !tracker.Unauthorized(authErr) {
		t.Fatalf("err = %v, want rejected credentials still classified through the hint", authErr)
	}
}

// The tracker user behind an API key never changes in practice, so re-resolving it
// on every tick only spends the budget the pull needs.
func TestSyncCachesIdentityAcrossTicks(t *testing.T) {
	fake := &fakeReader{synced: syncedFixture(), identityID: "u-42", identityName: "Grace Hopper"}
	ts, root, store := syncServer(t, fake)

	for range 3 {
		res, _ := postSync(t, ts, "acme")
		_ = res.Body.Close()
	}
	if fake.identityCalls != 1 {
		t.Fatalf("Identity called %d times, want the cached identity reused", fake.identityCalls)
	}

	if err := store.InvalidateIdentity(root); err != nil {
		t.Fatalf("InvalidateIdentity: %v", err)
	}
	res, _ := postSync(t, ts, "acme")
	_ = res.Body.Close()
	if fake.identityCalls != 2 {
		t.Fatalf("Identity called %d times, want an invalidated identity resolved again", fake.identityCalls)
	}
	st, err := store.SyncState(root)
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if st.Me.ID != "u-42" || st.Me.ResolvedAt == "" {
		t.Fatalf("me = %+v, want the identity resolved and stamped", st.Me)
	}
}

func TestSyncRejectedCredentialsExpireCachedIdentity(t *testing.T) {
	fake := &fakeReader{synced: syncedFixture(), identityID: "u-42", identityName: "Grace Hopper"}
	ts, root, store := syncServer(t, fake)

	res, _ := postSync(t, ts, "acme")
	_ = res.Body.Close()

	fake.syncErr = linearapi.ErrUnauthorized
	res, _ = postSync(t, ts, "acme")
	_ = res.Body.Close()

	st, err := store.SyncState(root)
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if st.Me.ResolvedAt != "" {
		t.Fatalf("me = %+v, want rejected credentials to expire the cached identity", st.Me)
	}
	if st.Me.ID != "u-42" {
		t.Fatalf("me = %+v, want the last known identity kept until one is re-resolved", st.Me)
	}

	fake.syncErr = nil
	res, _ = postSync(t, ts, "acme")
	_ = res.Body.Close()
	if fake.identityCalls != 2 {
		t.Fatalf("Identity called %d times, want the expired identity resolved again", fake.identityCalls)
	}
}

func TestIdentityStaleAfterTTL(t *testing.T) {
	fresh := hubstore.SyncIdentity{ID: "u-1", Name: "Ada", ResolvedAt: time.Now().Format(time.RFC3339Nano)}
	if identityStale(fresh) {
		t.Fatal("a just-resolved identity must not be re-resolved")
	}
	old := hubstore.SyncIdentity{ID: "u-1", Name: "Ada", ResolvedAt: time.Now().Add(-identityTTL - time.Minute).Format(time.RFC3339Nano)}
	if !identityStale(old) {
		t.Fatal("an identity past the TTL must be re-resolved")
	}
	if !identityStale(hubstore.SyncIdentity{ID: "u-1"}) {
		t.Fatal("an unstamped identity must be re-resolved")
	}
}

func TestTeamOverlapNamesUnfilteredBindings(t *testing.T) {
	tests := []struct {
		name      string
		binding   tracker.ProjectBinding
		bound     []hubstore.TeamBinding
		wantPeers []string
		wantWide  []string
	}{
		{
			name:     "alone on the team",
			binding:  tracker.ProjectBinding{TeamID: "team-1"},
			bound:    []hubstore.TeamBinding{{Repo: "/repo/loop"}},
			wantWide: []string{"/repo/loop"},
		},
		{
			name:      "peer pulls the whole team",
			binding:   tracker.ProjectBinding{TeamID: "team-1", ProjectID: "proj-1"},
			bound:     []hubstore.TeamBinding{{Repo: "/repo/loop", ProjectID: "proj-1"}, {Repo: "/repo/shipflock"}},
			wantPeers: []string{"/repo/shipflock"},
			wantWide:  []string{"/repo/shipflock"},
		},
		{
			name:      "this repo pulls the whole team",
			binding:   tracker.ProjectBinding{TeamID: "team-1"},
			bound:     []hubstore.TeamBinding{{Repo: "/repo/loop"}, {Repo: "/repo/shipflock", ProjectID: "proj-1"}},
			wantPeers: []string{"/repo/shipflock"},
			wantWide:  []string{"/repo/loop"},
		},
		{
			name:      "both narrowed to a project",
			binding:   tracker.ProjectBinding{TeamID: "team-1", ProjectID: "proj-1"},
			bound:     []hubstore.TeamBinding{{Repo: "/repo/loop", ProjectID: "proj-1"}, {Repo: "/repo/shipflock", ProjectID: "proj-2"}},
			wantPeers: []string{"/repo/shipflock"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			peers, wide := teamOverlap("/repo/loop", tt.binding, tt.bound)
			if strings.Join(peers, ",") != strings.Join(tt.wantPeers, ",") {
				t.Fatalf("peers = %v, want %v", peers, tt.wantPeers)
			}
			if strings.Join(wide, ",") != strings.Join(tt.wantWide, ",") {
				t.Fatalf("unfiltered = %v, want %v", wide, tt.wantWide)
			}
		})
	}
}

func TestSyncWarnsOnceAboutAWholeTeamPeer(t *testing.T) {
	var log bytes.Buffer
	logger.Init(&log, false, false)
	t.Cleanup(func() { logger.Init(os.Stderr, false, false) })

	fake := &fakeReader{synced: syncedFixture(), binding: tracker.ProjectBinding{TeamID: "team-1", ProjectID: "proj-1"}}
	ts, _, store := syncServer(t, fake)
	if err := store.SaveBinding("/repo/shipflock", hubstore.SyncBinding{TeamID: "team-1"}); err != nil {
		t.Fatalf("SaveBinding: %v", err)
	}

	for range 2 {
		res, _ := postSync(t, ts, "acme")
		_ = res.Body.Close()
	}

	if warnings := strings.Count(log.String(), "shares tracker team"); warnings != 1 {
		t.Fatalf("logged %d warnings in %q, want exactly one about the team-wide peer", warnings, log.String())
	}
	if !strings.Contains(log.String(), "team-1") || !strings.Contains(log.String(), "/repo/shipflock") {
		t.Fatalf("log = %q, want the shared team and the overlapping repo named", log.String())
	}
}

func TestSyncerRateLimitWaitsForResetWithoutFailing(t *testing.T) {
	sy := newSyncer(nil)
	root := "/repo/acme"

	sy.claim(root, time.Now())
	sy.settle(root, time.Minute, rateLimitErr(time.Now().Add(10*time.Minute)))

	if sy.claim(root, time.Now().Add(5*time.Minute)) {
		t.Fatal("a rate-limited repo should hold off until the budget window rolls")
	}
	if !sy.claim(root, time.Now().Add(11*time.Minute)) {
		t.Fatal("a rate-limited repo should resume once the window has rolled")
	}
	st := sy.state[root]
	if st.failures != 0 {
		t.Fatalf("failures = %d, want a rate limit to count as no failure", st.failures)
	}
}

func TestRateLimitWaitIsBounded(t *testing.T) {
	if got := rateLimitWait(time.Now().Add(-time.Minute), 2*time.Minute); got != 2*time.Minute {
		t.Fatalf("wait = %v, want a past reset floored at the interval", got)
	}
	if got := rateLimitWait(time.Now().Add(10*time.Hour), time.Minute); got != rateLimitCap {
		t.Fatalf("wait = %v, want the cap %v", got, rateLimitCap)
	}
	if got := rateLimitWait(time.Now().Add(10*time.Minute), time.Minute); got < 9*time.Minute || got > 10*time.Minute {
		t.Fatalf("wait = %v, want roughly the time to the reset", got)
	}
}

func TestSyncRateLimitStillReportsToTheCaller(t *testing.T) {
	fake := &fakeReader{syncErr: rateLimitErr(time.Now().Add(time.Minute))}
	ts, _, _ := syncServer(t, fake)

	res, _ := postSync(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want the manual sync to still report the refusal", res.StatusCode)
	}
}
