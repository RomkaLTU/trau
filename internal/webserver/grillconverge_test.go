package webserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

// newGrillPanelTest wires a runner whose claude and codex CLIs are both spawnable and
// whose repo config runs the given number of challenge rounds.
func newGrillPanelTest(t *testing.T, rounds string) (*grillRunner, *hubstore.Grill, hubstore.GrillSession) {
	t.Helper()
	r, store, repo, _ := newGrillRunnerTest(t, grillStubScript)
	appendGrillRunnerConfig(t, repo, "CODEX_BIN="+codexInstalledStub(t)+"\nGRILL_CHALLENGE_ROUNDS="+rounds+"\n")

	sess, err := store.Create(hubstore.NewGrillSession{
		Repo:        repo.Root,
		IssueID:     "COD-9",
		Mode:        hubstore.GrillModeInterview,
		Provider:    "claude",
		Challengers: []string{"codex"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return r, store, sess
}

// seedGrillPanelTurn records a panel turn the way the member's own submit_decision
// call would. The challenge children the runner spawns are stubs that cannot reach the
// hub, so the turns they would have recorded are laid down in front of them — the same
// stand-in the draft-phase test uses for a challenger's proposal.
func seedGrillPanelTurn(t *testing.T, r *grillRunner, sid int64, turn grillProposal) {
	t.Helper()
	if _, err := r.srv.appendGrillPanelTurn(sid, turn); err != nil {
		t.Fatalf("seed the %s turn: %v", turn.Provider, err)
	}
}

func grillOutcomeMessages(t *testing.T, store *hubstore.Grill, sid int64) []hubstore.GrillMessage {
	t.Helper()
	msgs, err := store.Messages(sid, 0)
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	out := []hubstore.GrillMessage{}
	for _, m := range msgs {
		if m.Kind == hubstore.GrillKindOutcome {
			out = append(out, m)
		}
	}
	return out
}

func decodeGrillDisagreement(t *testing.T, payload string) grillDisagreement {
	t.Helper()
	var wrapper struct {
		Disagreement grillDisagreement `json:"disagreement"`
	}
	if err := json.Unmarshal([]byte(payload), &wrapper); err != nil {
		t.Fatalf("decode disagreement: %v", err)
	}
	return wrapper.Disagreement
}

// A panel that converges in round 1 — the challenger endorses the interviewer's
// revision — settles the session itself: one canonical outcome carrying the winning
// proposal, a disagreement summary listing the opening split, and no proposal left for
// the user to choose between.
func TestGrillChallengeRoundConverges(t *testing.T) {
	r, store, sess := newGrillPanelTest(t, "2")
	seedGrillPanelTurn(t, r, sess.ID, grillProposal{
		Provider: "claude",
		Outcome:  json.RawMessage(`{"disposition":"rewrite","proposed_description":"first body","summary":"the interviewer's read"}`),
	})
	seedGrillPanelTurn(t, r, sess.ID, grillProposal{
		Provider: "codex",
		Outcome:  json.RawMessage(`{"disposition":"needs_split","summary":"this is epic-shaped"}`),
	})
	seedGrillPanelTurn(t, r, sess.ID, grillProposal{
		Provider:      "claude",
		Round:         1,
		Outcome:       json.RawMessage(`{"disposition":"split","proposed_description":"the epic goal","summary":"conceded the size"}`),
		ChallengeNote: "needs_split flags the size without slicing it",
	})
	seedGrillPanelTurn(t, r, sess.ID, grillProposal{Provider: "codex", Round: 1, Endorse: "claude"})

	r.runDrafts(sess)

	got, found, err := store.Session(sess.ID)
	if err != nil || !found {
		t.Fatalf("read session: %v (found = %v)", err, found)
	}
	if got.State != hubstore.GrillFinished {
		t.Fatalf("state = %q, want finished on the consensus", got.State)
	}
	outcomes := grillOutcomeMessages(t, store, sess.ID)
	if len(outcomes) != 1 {
		t.Fatalf("outcome rows = %d, want the single consensus outcome", len(outcomes))
	}

	// Apply reads the consensus exactly as it reads a solo proposal.
	outcome, ok := latestGrillOutcome(outcomes)
	if !ok || outcome.Disposition != "split" || outcome.ProposedDescription != "the epic goal" {
		t.Fatalf("consensus outcome = %+v, want the interviewer's round-1 revision", outcome)
	}

	summary := decodeGrillDisagreement(t, outcomes[0].Payload)
	if summary.Winner != "claude" {
		t.Fatalf("summary winner = %q, want claude", summary.Winner)
	}
	if len(summary.Initial) != 2 || summary.Initial[0].Disposition != "rewrite" || summary.Initial[1].Disposition != "needs_split" {
		t.Fatalf("summary opening split = %+v, want rewrite against needs_split", summary.Initial)
	}
	if len(summary.Rounds) != 1 {
		t.Fatalf("summary rounds = %+v, want the one round that converged", summary.Rounds)
	}
}

// A member that fails mid-round is dropped with a visible note, and the consensus is
// computed over the rest — here a panel of one, whose proposal carries the warning.
func TestGrillChallengeRoundDropsAFailedMember(t *testing.T) {
	r, store, sess := newGrillPanelTest(t, "1")
	seedGrillPanelTurn(t, r, sess.ID, grillProposal{
		Provider: "claude",
		Outcome:  json.RawMessage(`{"disposition":"rewrite","proposed_description":"first body","summary":"the interviewer's read"}`),
	})
	seedGrillPanelTurn(t, r, sess.ID, grillProposal{
		Provider: "codex",
		Outcome:  json.RawMessage(`{"disposition":"no_change","summary":"clear enough"}`),
	})
	// Only the interviewer's round-1 turn lands; the codex child records nothing.
	seedGrillPanelTurn(t, r, sess.ID, grillProposal{Provider: "claude", Round: 1, Endorse: "claude"})

	r.runDrafts(sess)

	outcomes := grillOutcomeMessages(t, store, sess.ID)
	if len(outcomes) != 1 {
		t.Fatalf("outcome rows = %d, want the surviving member's proposal promoted", len(outcomes))
	}
	outcome, _ := latestGrillOutcome(outcomes)
	if outcome.Disposition != "rewrite" {
		t.Fatalf("consensus disposition = %q, want the surviving interviewer's", outcome.Disposition)
	}
	summary := decodeGrillDisagreement(t, outcomes[0].Payload)
	if len(summary.Notes) == 0 || !strings.Contains(strings.Join(summary.Notes, "\n"), "codex") {
		t.Fatalf("summary notes = %v, want the dropped member named", summary.Notes)
	}

	msgs, err := store.Messages(sess.ID, 0)
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	dropped := false
	for _, m := range msgs {
		if m.Kind == hubstore.GrillKindInfo && strings.Contains(m.Payload, "dropped out of challenge round 1") {
			dropped = true
		}
	}
	if !dropped {
		t.Error("the drop left no visible note in the transcript")
	}
}

// The member endpoint's round segment is the panel's own contest: it takes the
// interviewer too, offers endorse, and refuses a revision that names nothing it
// disputes.
func TestGrillSubmitDecisionInChallengeRound(t *testing.T) {
	ts, stores, repo := grillServer(t)
	installChallengerCLIs(t)
	sess := createGrillWith(t, ts, repo, GrillCreateRequest{
		IssueID:     "COD-1",
		Provider:    "claude",
		Challengers: []string{"codex"},
	})
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

	round := memberMCPURL(ts, sess.ID, "codex") + "/1"
	if tr := toolResult(t, mcpJSON(t, round, toolCall("submit_decision", map[string]any{
		"disposition": "no_change",
		"summary":     "Changed my mind.",
	}))); !tr.IsError {
		t.Fatal("a revision with no challenge_note was accepted")
	}
	if tr := toolResult(t, mcpJSON(t, round, toolCall("submit_decision", map[string]any{
		"endorse": "kimi",
	}))); !tr.IsError {
		t.Fatal("an endorsement of a provider holding no proposal was accepted")
	}
	if tr := toolResult(t, mcpJSON(t, round, toolCall("submit_decision", map[string]any{
		"endorse": "claude",
	}))); tr.IsError {
		t.Fatalf("endorsing the interviewer returned an error: %+v", tr)
	}
	// One turn per member per round: the retry of a call the hub already answered is
	// refused rather than counted twice.
	if tr := toolResult(t, mcpJSON(t, round, toolCall("submit_decision", map[string]any{
		"endorse": "claude",
	}))); !tr.IsError {
		t.Fatal("a second turn in the same round was accepted")
	}

	// The interviewer answers the same contest through its own seat.
	interviewer := memberMCPURL(ts, sess.ID, "claude") + "/1"
	if tr := toolResult(t, mcpJSON(t, interviewer, toolCall("submit_decision", map[string]any{
		"endorse": "claude",
	}))); tr.IsError {
		t.Fatalf("the interviewer's round turn returned an error: %+v", tr)
	}
	// It has no draft-phase seat, though: the drafts are the challengers' alone.
	res := postJSON(t, memberMCPURL(ts, sess.ID, "claude"), toolCall("submit_decision", map[string]any{
		"disposition": "no_change",
		"summary":     "clear",
	}))
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("interviewer draft-phase status = %d, want 404", res.StatusCode)
	}

	sid, err := strconv.ParseInt(sess.ID, 10, 64)
	if err != nil {
		t.Fatalf("parse sid: %v", err)
	}
	msgs, err := stores.Grill().Messages(sid, 0)
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	cycle := grillActiveCycle(msgs)
	if winner, ok := grillConsensus([]string{"claude", "codex"}, grillRoundVotes(cycle, 1)); !ok || winner != "claude" {
		t.Fatalf("round 1 resolved to (%q, %v), want a unanimous claude", winner, ok)
	}
	if n := len(grillCurrentProposals(cycle)); n != 2 {
		t.Fatalf("current proposals = %d, want one per member — an endorsement moves a vote, not a draft", n)
	}
}
