package webserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

// installChallengerCLIs makes codex and kimi spawnable for the grillServer repo, which
// is what the create path checks before it locks a second opinion in.
func installChallengerCLIs(t *testing.T) {
	t.Helper()
	seedKimiConfig(t, "k3")
	writeGrillConfig(t, "KIMI_BIN="+installedStub(t, "kimi")+"\nCODEX_BIN="+codexInstalledStub(t)+"\n")
}

func memberMCPURL(ts *httptest.Server, sid, member string) string {
	return ts.URL + APIPrefix + "/grill/" + sid + "/mcp/" + member
}

func TestGrillCreateWithChallengers(t *testing.T) {
	ts, _, repo := grillServer(t)
	installChallengerCLIs(t)

	sess := createGrillWith(t, ts, repo, GrillCreateRequest{
		IssueID:     "COD-1",
		Provider:    "claude",
		Challengers: []string{"codex", "kimi"},
	})
	if !slices.Equal(sess.Challengers, []string{"codex", "kimi"}) {
		t.Fatalf("created challengers = %v, want [codex kimi]", sess.Challengers)
	}
	if sess.Mode != hubstore.GrillModeInterview {
		t.Fatalf("mode = %q, want interview", sess.Mode)
	}
	if got := grillDetail(t, ts, sess.ID).Session.Challengers; !slices.Equal(got, []string{"codex", "kimi"}) {
		t.Fatalf("detail challengers = %v, want [codex kimi]", got)
	}
}

func TestGrillCreateRejectsBadChallengers(t *testing.T) {
	ts, _, repo := grillServer(t)
	installChallengerCLIs(t)

	cases := []struct {
		name string
		req  GrillCreateRequest
	}{
		{"outside interview mode", GrillCreateRequest{IssueID: "COD-1", Mode: hubstore.GrillModeResearch, Challengers: []string{"codex"}}},
		{"unknown provider", GrillCreateRequest{IssueID: "COD-1", Challengers: []string{"bogus"}}},
		{"duplicated", GrillCreateRequest{IssueID: "COD-1", Challengers: []string{"codex", "codex"}}},
		{"the interviewer itself", GrillCreateRequest{IssueID: "COD-1", Provider: "codex", Challengers: []string{"codex"}}},
		{"more than two", GrillCreateRequest{IssueID: "COD-1", Challengers: []string{"codex", "kimi", "claude"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", tc.req)
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("create status = %d, want 400", res.StatusCode)
			}
		})
	}
}

func TestGrillCreateRejectsUnavailableChallenger(t *testing.T) {
	ts, _, repo := grillServer(t)
	writeGrillConfig(t, "CODEX_BIN="+filepath.Join(t.TempDir(), "no-such-codex")+"\n")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/grill", GrillCreateRequest{
		IssueID:     "COD-1",
		Challengers: []string{"codex"},
	})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("create status = %d, want 400 for a CLI this machine cannot spawn", res.StatusCode)
	}
}

// The interviewer's finish_session on a second-opinion session records a proposal and
// leaves the session running: the draft phase, not the tool call, ends it.
func TestGrillFinishRecordsProposalWithChallengers(t *testing.T) {
	ts, _, repo := grillServer(t)
	installChallengerCLIs(t)
	sess := createGrillWith(t, ts, repo, GrillCreateRequest{
		IssueID:     "COD-1",
		Provider:    "claude",
		Challengers: []string{"codex"},
	})

	msg := mcpJSON(t, mcpURL(ts, sess.ID), toolCall("finish_session", map[string]any{
		"disposition":          "rewrite",
		"proposed_description": "Reset the password from the login page.",
		"summary":              "Clarified the reset flow.",
	}))
	if tr := toolResult(t, msg); tr.IsError {
		t.Fatalf("finish returned an error: %+v", tr)
	}

	detail := grillDetail(t, ts, sess.ID)
	if detail.Session.State != hubstore.GrillRunning {
		t.Fatalf("state = %q, want running while the drafts are owed", detail.Session.State)
	}
	last := detail.Messages[len(detail.Messages)-1]
	if last.Kind != hubstore.GrillKindProposal {
		t.Fatalf("last message kind = %q, want proposal", last.Kind)
	}
	var proposal struct {
		Provider string `json:"provider"`
		Round    int    `json:"round"`
		Outcome  struct {
			Disposition         string `json:"disposition"`
			ProposedDescription string `json:"proposed_description"`
		} `json:"outcome"`
	}
	if err := json.Unmarshal(last.Payload, &proposal); err != nil {
		t.Fatalf("decode proposal: %v", err)
	}
	if proposal.Provider != "claude" || proposal.Round != 0 {
		t.Fatalf("proposal = %+v, want the interviewer at round 0", proposal)
	}
	if proposal.Outcome.Disposition != "rewrite" || proposal.Outcome.ProposedDescription == "" {
		t.Fatalf("proposal outcome = %+v", proposal.Outcome)
	}
}

// A challenger submits through its own member endpoint, the user picks one of the two
// proposals, and the choice mints the canonical outcome the ordinary review applies.
func TestGrillChooseProposal(t *testing.T) {
	ts, stores, repo := grillServer(t)
	installChallengerCLIs(t)
	sess := createGrillWith(t, ts, repo, GrillCreateRequest{
		IssueID:     "COD-1",
		Provider:    "claude",
		Challengers: []string{"codex"},
	})
	sid, err := strconv.ParseInt(sess.ID, 10, 64)
	if err != nil {
		t.Fatalf("parse sid: %v", err)
	}

	if tr := toolResult(t, mcpJSON(t, mcpURL(ts, sess.ID), toolCall("finish_session", map[string]any{
		"disposition":          "rewrite",
		"proposed_description": "The interviewer's body.",
		"summary":              "Interviewer summary.",
	}))); tr.IsError {
		t.Fatalf("finish returned an error: %+v", tr)
	}
	if tr := toolResult(t, mcpJSON(t, memberMCPURL(ts, sess.ID, "codex"), toolCall("submit_decision", map[string]any{
		"disposition": "needs_split",
		"summary":     "This is epic-shaped.",
	}))); tr.IsError {
		t.Fatalf("submit_decision returned an error: %+v", tr)
	}

	// The draft phase spawns real children, so the test stands in for it: what matters
	// here is the review that follows every challenger reporting in.
	if _, err := stores.Grill().Transition(sid, hubstore.GrillFinished, ""); err != nil {
		t.Fatalf("finish session: %v", err)
	}

	detail := grillDetail(t, ts, sess.ID)
	proposals := []GrillMessageView{}
	for _, m := range detail.Messages {
		if m.Kind == hubstore.GrillKindProposal {
			proposals = append(proposals, m)
		}
	}
	if len(proposals) != 2 {
		t.Fatalf("proposals = %d, want 2", len(proposals))
	}

	// Nothing is applicable until one of them is chosen.
	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/apply", GrillApplyRequest{})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("apply status = %d, want 409 before a proposal is chosen", res.StatusCode)
	}

	chosen := proposals[1]
	res = postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/choose-proposal", GrillChooseProposalRequest{MessageID: chosen.ID})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("choose status = %d, want 200", res.StatusCode)
	}
	var picked GrillChooseProposalResponse
	if err := json.NewDecoder(res.Body).Decode(&picked); err != nil {
		t.Fatalf("decode choose: %v", err)
	}
	if picked.Message.Kind != hubstore.GrillKindOutcome {
		t.Fatalf("chosen message kind = %q, want outcome", picked.Message.Kind)
	}
	var outcome struct {
		Disposition string `json:"disposition"`
		Summary     string `json:"summary"`
	}
	if err := json.Unmarshal(picked.Message.Payload, &outcome); err != nil {
		t.Fatalf("decode outcome: %v", err)
	}
	if outcome.Disposition != "needs_split" || outcome.Summary != "This is epic-shaped." {
		t.Fatalf("outcome = %+v, want the challenger's own decision", outcome)
	}

	// A second choice has nothing left to decide.
	again := postJSON(t, ts.URL+APIPrefix+"/grill/"+sess.ID+"/choose-proposal", GrillChooseProposalRequest{MessageID: chosen.ID})
	_ = again.Body.Close()
	if again.StatusCode != http.StatusConflict {
		t.Fatalf("second choose status = %d, want 409", again.StatusCode)
	}
}

// The draft phase is what ends a second-opinion session with GRILL_CHALLENGE_ROUNDS=0:
// each challenger is spawned against its own submit_decision endpoint, the panel is
// told a draft is running, and the session only reaches finished once every challenger
// is accounted for — with the surviving proposals left for the user to choose between,
// and no challenge round in between. The challenger's own submit_decision call is
// TestGrillChooseProposal's ground, so its proposal is already on the record here and
// this reads the phase around it.
func TestGrillChallengerDraftsFinishTheSession(t *testing.T) {
	r, store, repo, stubDir := newGrillRunnerTest(t, grillStubScript)
	appendGrillRunnerConfig(t, repo, "GRILL_CHALLENGE_ROUNDS=0\n")
	sess, err := store.Create(hubstore.NewGrillSession{
		Repo:        repo.Root,
		IssueID:     "COD-7",
		Mode:        hubstore.GrillModeInterview,
		Provider:    "codex",
		Challengers: []string{"claude"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, p := range []struct{ provider, outcome string }{
		{"codex", `{"disposition":"no_change","summary":"The issue already says enough."}`},
		{"claude", `{"disposition":"needs_split","summary":"This is epic-shaped."}`},
	} {
		if _, err := r.srv.appendGrillProposal(sess.ID, p.provider, p.outcome); err != nil {
			t.Fatalf("seed the %s proposal: %v", p.provider, err)
		}
	}

	sub, ch := r.srv.grillEvents.subscribe()
	defer r.srv.grillEvents.unsubscribe(sub)

	r.runDrafts(sess)

	got, found, err := store.Session(sess.ID)
	if err != nil || !found {
		t.Fatalf("read session: %v (found = %v)", err, found)
	}
	if got.State != hubstore.GrillFinished {
		t.Fatalf("state = %q, want finished once every draft is in", got.State)
	}
	msgs, err := store.Messages(sess.ID, 0)
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	if n := len(grillProposalViews(msgs)); n != 2 {
		t.Fatalf("proposals = %d, want the two the session collected", n)
	}
	if grillDecided(msgs) {
		t.Error("the phase settled a canonical outcome; two surviving proposals are the user's to choose between")
	}

	// The draft child talks to the member's own submit_decision surface, not the
	// session's interviewer tools.
	args := readNullArgs(t, filepath.Join(stubDir, "first.args"))
	member := fmt.Sprintf("%s/grill/%d/mcp/claude", APIPrefix, sess.ID)
	if !slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, member) }) {
		t.Fatalf("draft args = %v, want the member endpoint %q", args, member)
	}

	var opened, result *GrillActivityView
	for draining := true; draining; {
		select {
		case ev := <-ch:
			act, ok := ev.Payload.(GrillActivityView)
			if !ok || act.ID != grillDraftActivityID("claude") {
				continue
			}
			if act.Kind == grillActivityResult {
				result = &act
				continue
			}
			opened = &act
		default:
			draining = false
		}
	}
	if opened == nil || opened.Name != "claude" || opened.Detail == "" {
		t.Fatalf("draft activity = %+v, want the frame saying claude is drafting", opened)
	}
	if result == nil || result.OK == nil || !*result.OK {
		t.Fatalf("draft result activity = %+v, want it resolved ok", result)
	}
}

func TestGrillMemberMCPRejectsUnknownMember(t *testing.T) {
	ts, _, repo := grillServer(t)
	installChallengerCLIs(t)
	sess := createGrillWith(t, ts, repo, GrillCreateRequest{
		IssueID:     "COD-1",
		Provider:    "claude",
		Challengers: []string{"codex"},
	})

	res := postJSON(t, memberMCPURL(ts, sess.ID, "kimi"), toolCall("submit_decision", map[string]any{
		"disposition": "no_change",
		"summary":     "clear",
	}))
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown member status = %d, want 404", res.StatusCode)
	}
}
