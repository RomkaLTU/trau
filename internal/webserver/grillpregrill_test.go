package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

// newPregrillServer builds a Server over a real store and a registered repo, with
// runGrillTurn stubbed to settle each session to the state run wants — the pass
// under test never spawns a real claude.
func newPregrillServer(t *testing.T, run func(*Server, hubstore.GrillSession)) (*Server, registry.Repo) {
	t.Helper()
	home := t.TempDir()
	stores := testStoresAt(t, home)
	repo := registry.Repo{Name: "acme", Root: filepath.Join(home, "acme"), RunsDir: filepath.Join(home, "acme", ".trau", "runs")}
	if err := os.MkdirAll(repo.Root, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := stores.Registrations().Remember([]registry.Repo{repo}); err != nil {
		t.Fatalf("remember repo: %v", err)
	}
	srv := New("test", "127.0.0.1", "", nil, false, stores)
	if run != nil {
		srv.runGrillTurn = func(_ context.Context, sess hubstore.GrillSession) { run(srv, sess) }
	}
	return srv, repo
}

// parkTurn simulates the common pre-grill turn: the agent asked its opening question
// and the AFK idle-park left the session parked with no reason (question waiting).
func parkTurn(s *Server, sess hubstore.GrillSession) {
	if _, _, err := s.stores.Grill().AppendMessage(sess.ID, hubstore.NewGrillMessage{
		Role: hubstore.GrillRoleAgent, Kind: hubstore.GrillKindQuestion, Payload: `{"text":"which flow?"}`,
	}); err != nil {
		panic(err)
	}
	if _, err := s.stores.Grill().Transition(sess.ID, hubstore.GrillParked, ""); err != nil {
		panic(err)
	}
}

// finishTurn simulates a turn that proposed an outcome with disposition and settled
// the session finished.
func finishTurn(disposition string) func(*Server, hubstore.GrillSession) {
	return func(s *Server, sess hubstore.GrillSession) {
		payload, _ := json.Marshal(map[string]string{"disposition": disposition, "summary": "done"})
		if _, _, err := s.stores.Grill().AppendMessage(sess.ID, hubstore.NewGrillMessage{
			Role: hubstore.GrillRoleAgent, Kind: hubstore.GrillKindOutcome, Payload: string(payload),
		}); err != nil {
			panic(err)
		}
		if _, err := s.stores.Grill().Transition(sess.ID, hubstore.GrillFinished, ""); err != nil {
			panic(err)
		}
	}
}

func TestClassifyPregrillOutcome(t *testing.T) {
	tests := []struct {
		name        string
		state       string
		reason      string
		disposition string
		wantOutcome string
	}{
		{name: "idle park is a waiting question", state: hubstore.GrillParked, wantOutcome: pregrillOutcomeQuestion},
		{name: "waiting is a waiting question", state: hubstore.GrillWaiting, wantOutcome: pregrillOutcomeQuestion},
		{name: "rewrite proposal", state: hubstore.GrillFinished, disposition: grillDispRewrite, wantOutcome: pregrillOutcomeRewrite},
		{name: "split proposal is a draft", state: hubstore.GrillFinished, disposition: grillDispSplit, wantOutcome: pregrillOutcomeRewrite},
		{name: "no_change is clear", state: hubstore.GrillFinished, disposition: grillDispNoChange, wantOutcome: pregrillOutcomeClear},
		{name: "crash park is an error", state: hubstore.GrillParked, reason: grillCrashReason, wantOutcome: pregrillOutcomeError},
		{name: "stall is an error", state: hubstore.GrillStalled, reason: "needs re-authentication", wantOutcome: pregrillOutcomeError},
		{name: "still running is an error", state: hubstore.GrillRunning, wantOutcome: pregrillOutcomeError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := hubstore.GrillSession{State: tt.state, ParkedReason: tt.reason}
			got, _ := classifyPregrillOutcome(sess, tt.disposition)
			if got != tt.wantOutcome {
				t.Errorf("outcome = %q, want %q", got, tt.wantOutcome)
			}
		})
	}
}

func TestPregrillPassBounding(t *testing.T) {
	srv, repo := newPregrillServer(t, parkTurn)

	ids := []string{"COD-1", "COD-2", "COD-3", "COD-4", "COD-5", "COD-6", "COD-7", "COD-8"}
	results := srv.runPregrillPass(context.Background(), repo, ids, 5)

	if len(results) != len(ids) {
		t.Fatalf("results = %d, want one per issue (%d)", len(results), len(ids))
	}
	grilled := 0
	for _, r := range results[:5] {
		if r.Outcome != pregrillOutcomeQuestion {
			t.Errorf("%s outcome = %q, want %q", r.IssueID, r.Outcome, pregrillOutcomeQuestion)
		}
		grilled++
	}
	for _, r := range results[5:] {
		if r.Outcome != pregrillOutcomeSkipped {
			t.Errorf("%s beyond the limit = %q, want skipped", r.IssueID, r.Outcome)
		}
	}
	if grilled != 5 {
		t.Fatalf("grilled %d issues, want the 5-turn budget", grilled)
	}
	sessions, err := srv.stores.Grill().List(repo.Root, "")
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 5 {
		t.Fatalf("created %d sessions, want the pass to spawn only 5", len(sessions))
	}
}

func TestPregrillPassSkipsActiveSession(t *testing.T) {
	srv, repo := newPregrillServer(t, parkTurn)

	if _, err := srv.stores.Grill().Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: "COD-2"}); err != nil {
		t.Fatalf("seed active session: %v", err)
	}

	results := srv.runPregrillPass(context.Background(), repo, []string{"COD-1", "COD-2", "COD-3"}, 5)

	byIssue := map[string]PregrillResult{}
	for _, r := range results {
		byIssue[r.IssueID] = r
	}
	if got := byIssue["COD-2"].Outcome; got != pregrillOutcomeSkipped {
		t.Errorf("COD-2 (already active) outcome = %q, want skipped", got)
	}
	if got := byIssue["COD-1"].Outcome; got != pregrillOutcomeQuestion {
		t.Errorf("COD-1 outcome = %q, want %q", got, pregrillOutcomeQuestion)
	}
	if got := byIssue["COD-3"].Outcome; got != pregrillOutcomeQuestion {
		t.Errorf("COD-3 outcome = %q, want %q", got, pregrillOutcomeQuestion)
	}

	sessions, err := srv.stores.Grill().List(repo.Root, "")
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("sessions = %d, want the seeded one plus two grilled", len(sessions))
	}
}

func TestPregrillPassClassifiesFinishOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		disposition string
		wantOutcome string
	}{
		{name: "rewrite", disposition: grillDispRewrite, wantOutcome: pregrillOutcomeRewrite},
		{name: "no_change", disposition: grillDispNoChange, wantOutcome: pregrillOutcomeClear},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, repo := newPregrillServer(t, finishTurn(tt.disposition))
			results := srv.runPregrillPass(context.Background(), repo, []string{"COD-9"}, 5)
			if len(results) != 1 {
				t.Fatalf("results = %d, want 1", len(results))
			}
			if results[0].Outcome != tt.wantOutcome {
				t.Errorf("outcome = %q, want %q", results[0].Outcome, tt.wantOutcome)
			}
		})
	}
}

// TestPregrillHandlerBoundsFromConfig proves the wiring end to end: the endpoint is
// registered, honours GRILL_PREGRILL_MAX from repo config, and drives the pass.
func TestPregrillHandlerBoundsFromConfig(t *testing.T) {
	srv, repo := newPregrillServer(t, parkTurn)
	if err := os.WriteFile(config.ProjectConfigPath(repo.Root), []byte("GRILL_PREGRILL_MAX=2\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo.Name+"/grill/pregrill", PregrillRequest{
		IssueIDs: []string{"COD-1", "COD-2", "COD-3"},
	})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var got PregrillResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Max != 2 {
		t.Errorf("max = %d, want 2 from config", got.Max)
	}
	grilled := 0
	for _, r := range got.Results {
		if r.Outcome == pregrillOutcomeQuestion {
			grilled++
		}
	}
	if grilled != 2 {
		t.Fatalf("grilled %d, want the configured 2", grilled)
	}
}
