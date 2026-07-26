package webserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/state"
)

// TestTeamSyncSharesRunHistoryBetweenMachines is the end-to-end contract: a run
// settled on one machine reaches the other machine's ledger attributed to the
// author who ran it, carrying the outcome, the timing, the derived Activity
// timeline, and the cost — and nothing that would need a remote transcript.
func TestTeamSyncSharesRunHistoryBetweenMachines(t *testing.T) {
	remote, peers := newTeamFixture(t, true, "ada", "grace")
	ada, grace := peers["ada"], peers["grace"]

	settleRun(t, ada, "COD-70", map[string]string{
		"PHASE":   state.Merged,
		"TITLE":   "share the ledger",
		"BRANCH":  "feature/COD-70",
		"PR_URL":  "https://example.test/pr/70",
		"UPDATED": "2026-07-26T10:05:30Z",
	}, 330)

	waitForTeamRefs(t, remote, 1)
	syncNow(t, grace.ts, "grace")

	runs := getTeamRuns(t, grace.ts, "grace")
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want the teammate's run folded in", runs)
	}
	got := runs[0]
	if got.Shared == nil {
		t.Fatalf("run = %+v, want it marked as a teammate's", got)
	}
	if got.Shared.Author != "Ada" {
		t.Errorf("author = %q, want the runner's git name", got.Shared.Author)
	}
	if got.Ticket != "COD-70" || got.Title != "share the ledger" {
		t.Errorf("run = %+v, want the ticket and title carried", got)
	}
	if got.Phase != state.Merged || !got.Terminal {
		t.Errorf("outcome = %q terminal=%v, want a merged run", got.Phase, got.Terminal)
	}
	if got.Branch != "feature/COD-70" || got.PRURL != "https://example.test/pr/70" {
		t.Errorf("run = %+v, want the branch and PR url carried", got)
	}
	if got.Shared.StartedAt != "2026-07-26T10:00:00Z" || got.Shared.EndedAt != "2026-07-26T10:05:30Z" {
		t.Errorf("timing = %q..%q, want the run's start and end", got.Shared.StartedAt, got.Shared.EndedAt)
	}
	if got.Shared.Provider != "claude" || got.Shared.Model != "opus" {
		t.Errorf("route = %q/%q, want the provider and model carried", got.Shared.Provider, got.Shared.Model)
	}
	if got.Shared.Tokens != 4000 {
		t.Errorf("tokens = %d, want the run's token total", got.Shared.Tokens)
	}
	if got.CostUSD == nil || *got.CostUSD != 1.5 {
		t.Errorf("cost = %v, want the run's spend so the totals combine", got.CostUSD)
	}
	if want := []ActivitySpan{
		{Activity: "build", DurationMS: 120_000},
		{Activity: "verify", DurationMS: 60_000},
		{Activity: "commit", DurationMS: 150_000},
	}; !equalSpans(got.Shared.Activities, want) {
		t.Errorf("timeline = %+v, want %+v", got.Shared.Activities, want)
	}
	if got.Handback != nil || got.PRStatus != "" {
		t.Errorf("run = %+v, want only the fields that travelled", got)
	}

	// A teammate's history is read-only and display-only: it reaches neither the
	// checkpoints table the picker, queue, and drain read, nor this machine's own
	// runs resource, whose readers all mean local work.
	if rows, err := grace.stores.Checkpoints().All(grace.root); err != nil || len(rows) != 0 {
		t.Errorf("checkpoints = %+v (err %v), want nothing written locally", rows, err)
	}
	if local := getRuns(t, grace.ts, "grace").Runs; len(local) != 0 {
		t.Errorf("runs = %+v, want the local ledger untouched", local)
	}
}

// TestSharedRunHistoryRespectsTheCap pins the snapshot bound: a machine with more
// settled runs than sharedRunCap publishes only its most recent ones, so the
// snapshot commit stays small however long it has been running.
func TestSharedRunHistoryRespectsTheCap(t *testing.T) {
	remote, peers := newTeamFixture(t, true, "ada", "grace")
	ada, grace := peers["ada"], peers["grace"]

	for i := range sharedRunCap + 10 {
		ticket := fmt.Sprintf("COD-%03d", i)
		if err := ada.stores.Checkpoints().Upsert(ada.root, ticket, map[string]string{
			"PHASE":   state.Merged,
			"UPDATED": time.Date(2026, 7, 26, 0, i, 0, 0, time.UTC).Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("seed checkpoint %s: %v", ticket, err)
		}
	}
	// A run still in flight has no outcome to share and must never travel.
	if err := ada.stores.Checkpoints().Upsert(ada.root, "COD-999", map[string]string{
		"PHASE": state.Building, "UPDATED": "2026-07-27T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed in-flight checkpoint: %v", err)
	}

	syncNow(t, ada.ts, "ada")
	waitForTeamRefs(t, remote, 1)
	syncNow(t, grace.ts, "grace")

	runs := getTeamRuns(t, grace.ts, "grace")
	if len(runs) != sharedRunCap {
		t.Fatalf("shared runs = %d, want the cap of %d", len(runs), sharedRunCap)
	}
	for _, run := range runs {
		if run.Ticket < "COD-010" {
			t.Errorf("shared run %q, want only the most recent %d", run.Ticket, sharedRunCap)
		}
		if run.Ticket == "COD-999" {
			t.Error("an in-flight run travelled")
		}
	}
}

// TestRunLedgerUnchangedWithTeamSyncOff pins the opt-in: with the toggle off there
// is no shared history at all, so the ledger renders exactly as it did before
// sharing existed — including on a repo that shared before, whose teammates'
// records this machine already fetched and still stores.
func TestRunLedgerUnchangedWithTeamSyncOff(t *testing.T) {
	remote, peers := newTeamFixture(t, true, "ada", "grace")
	ada, grace := peers["ada"], peers["grace"]

	settleRun(t, ada, "COD-70", map[string]string{
		"PHASE": state.Merged, "UPDATED": "2026-07-26T10:05:30Z",
	}, 330)
	waitForTeamRefs(t, remote, 1)
	syncNow(t, grace.ts, "grace")
	if shared := getTeamRuns(t, grace.ts, "grace"); len(shared) != 1 {
		t.Fatalf("team runs = %+v, want the teammate's run while sharing is on", shared)
	}

	writeTeamConfig(t, grace.root, "TEAM_SYNC=0\n")
	settleRun(t, grace, "COD-71", map[string]string{
		"PHASE": state.Merged, "UPDATED": "2026-07-26T11:00:00Z",
	}, 330)

	if shared := getTeamRuns(t, grace.ts, "grace"); len(shared) != 0 {
		t.Errorf("team runs = %+v, want none with team sync off", shared)
	}
	runs := getRuns(t, grace.ts, "grace").Runs
	if len(runs) != 1 || runs[0].Ticket != "COD-71" || runs[0].Shared != nil {
		t.Fatalf("runs = %+v, want this machine's own row alone", runs)
	}
}

// settleRun records one finished run on a peer exactly as its loop would: the
// checkpoint, the token calls it spent, and the activity/state events its
// wall-clock derives from. The terminal state_change is what triggers the publish.
func settleRun(t *testing.T, peer *teamPeer, ticket string, cp map[string]string, endSecs int) {
	t.Helper()
	if err := peer.stores.Checkpoints().Upsert(peer.root, ticket, cp); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
	cost := 1.5
	if err := peer.stores.Tokens().Append(peer.root, []hubstore.TokenCall{{
		Ticket: ticket, TS: "2026-07-26T10:01:00Z", Phase: "build", Total: 4000,
		CostUSD: &cost, Provider: "claude", Model: "opus",
	}}); err != nil {
		t.Fatalf("seed token call: %v", err)
	}

	base := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	stamp := func(secs int) string { return base.Add(time.Duration(secs) * time.Second).Format(time.RFC3339) }
	fields := func(extra string) string { return `{"ticket":"` + ticket + `",` + extra + `}` }
	postEvents(t, peer.ts, peer.name,
		hubclient.Event{TS: stamp(0), Kind: "activity_change", Fields: fields(`"activity":"build"`)},
		hubclient.Event{TS: stamp(120), Kind: "activity_change", Fields: fields(`"activity":"verify"`)},
		hubclient.Event{TS: stamp(180), Kind: "activity_change", Fields: fields(`"activity":"commit"`)},
		hubclient.Event{TS: stamp(endSecs), Kind: "state_change", Fields: fields(`"state":"merged"`)},
	)
}

func getTeamRuns(t *testing.T, ts *httptest.Server, repo string) []RunView {
	t.Helper()
	res, body := get(t, ts, APIPrefix+"/repos/"+repo+"/team-runs")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET team-runs status = %d, want 200", res.StatusCode)
	}
	var out TeamRunsResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode team runs: %v", err)
	}
	return out.Runs
}

func equalSpans(got, want []ActivitySpan) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
