package webserver

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

func TestGrillConsensus(t *testing.T) {
	cases := []struct {
		name    string
		members []string
		votes   []grillRoundVote
		want    string
		wantOK  bool
	}{
		{
			name:    "a challenger endorses the interviewer",
			members: []string{"claude", "codex"},
			votes: []grillRoundVote{
				{Provider: "claude"},
				{Provider: "codex", Endorse: "claude"},
			},
			want:   "claude",
			wantOK: true,
		},
		{
			name:    "everybody endorses a third member's proposal",
			members: []string{"claude", "codex", "kimi"},
			votes: []grillRoundVote{
				{Provider: "claude", Endorse: "kimi"},
				{Provider: "codex", Endorse: "kimi"},
				{Provider: "kimi", Endorse: "kimi"},
			},
			want:   "kimi",
			wantOK: true,
		},
		{
			name:    "both revise and hold their own",
			members: []string{"claude", "codex"},
			votes: []grillRoundVote{
				{Provider: "claude"},
				{Provider: "codex"},
			},
			wantOK: false,
		},
		{
			name:    "one endorses and one holds out",
			members: []string{"claude", "codex"},
			votes: []grillRoundVote{
				{Provider: "claude", Endorse: "codex"},
				{Provider: "codex"},
			},
			want:   "codex",
			wantOK: true,
		},
		{
			name:    "a member that said nothing keeps the round open",
			members: []string{"claude", "codex"},
			votes:   []grillRoundVote{{Provider: "claude", Endorse: "claude"}},
			wantOK:  false,
		},
		{
			name:    "an endorsement of a member no longer on the panel resolves nothing",
			members: []string{"claude", "codex"},
			votes: []grillRoundVote{
				{Provider: "claude", Endorse: "kimi"},
				{Provider: "codex", Endorse: "kimi"},
			},
			wantOK: false,
		},
		{
			name:    "the last member left is its own consensus",
			members: []string{"codex"},
			votes:   []grillRoundVote{{Provider: "codex"}},
			want:    "codex",
			wantOK:  true,
		},
		{
			name:    "an empty panel decides nothing",
			members: []string{},
			votes:   []grillRoundVote{{Provider: "codex", Endorse: "codex"}},
			wantOK:  false,
		},
		{
			name:    "a vote from outside the panel is not counted",
			members: []string{"claude", "codex"},
			votes: []grillRoundVote{
				{Provider: "claude", Endorse: "claude"},
				{Provider: "codex", Endorse: "claude"},
				{Provider: "kimi", Endorse: "codex"},
			},
			want:   "claude",
			wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := grillConsensus(tc.members, tc.votes)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("grillConsensus() = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// A follow-up answer on a finished panel session opens a new cycle: everything before
// it is history, so the drafts and rounds that run next are read on their own.
func TestGrillActiveCycle(t *testing.T) {
	msgs := []hubstore.GrillMessage{
		{ID: 1, Role: hubstore.GrillRoleUser, Kind: hubstore.GrillKindInfo},
		{ID: 2, Role: hubstore.GrillRoleAgent, Kind: hubstore.GrillKindQuestion},
		{ID: 3, Role: hubstore.GrillRoleUser, Kind: hubstore.GrillKindAnswer},
		{ID: 4, Role: hubstore.GrillRoleAgent, Kind: hubstore.GrillKindProposal},
		{ID: 5, Role: hubstore.GrillRoleAgent, Kind: hubstore.GrillKindOutcome},
		{ID: 6, Role: hubstore.GrillRoleUser, Kind: hubstore.GrillKindAnswer},
		{ID: 7, Role: hubstore.GrillRoleAgent, Kind: hubstore.GrillKindProposal},
		{ID: 8, Role: hubstore.GrillRoleUser, Kind: hubstore.GrillKindInterjection},
	}
	cycle := grillActiveCycle(msgs)
	if len(cycle) != 3 || cycle[0].ID != 6 {
		t.Fatalf("cycle = %v, want the three rows from the reopening answer on", cycle)
	}
	if got := grillActiveCycle(msgs[:5]); len(got) != 5 {
		t.Fatalf("a session that never reopened has one cycle, got %d of 5 rows", len(got))
	}
}

// The disagreement summary is assembled from the recorded rounds alone: the opening
// split, then who endorsed what and who revised with which note.
func TestGrillDisagreementSummary(t *testing.T) {
	turns := []GrillProposalView{
		{Provider: "claude", Round: 0, Outcome: json.RawMessage(`{"disposition":"rewrite"}`)},
		{Provider: "codex", Round: 0, Outcome: json.RawMessage(`{"disposition":"needs_split"}`)},
		{Provider: "claude", Round: 1, Outcome: json.RawMessage(`{"disposition":"split"}`), ChallengeNote: "it is epic-shaped"},
		{Provider: "codex", Round: 1, Endorse: "claude"},
	}
	got := grillDisagreementSummary(turns, "claude", []string{"kimi dropped out"})

	if got.Winner != "claude" {
		t.Fatalf("winner = %q, want claude", got.Winner)
	}
	want := []grillDisposition{{Provider: "claude", Disposition: "rewrite"}, {Provider: "codex", Disposition: "needs_split"}}
	if !slices.Equal(got.Initial, want) {
		t.Fatalf("initial = %+v, want %+v", got.Initial, want)
	}
	if len(got.Rounds) != 1 || got.Rounds[0].Round != 1 || len(got.Rounds[0].Turns) != 2 {
		t.Fatalf("rounds = %+v, want one round holding both turns", got.Rounds)
	}
	revision, endorsement := got.Rounds[0].Turns[0], got.Rounds[0].Turns[1]
	if revision.Disposition != "split" || revision.Note != "it is epic-shaped" || revision.Endorse != "" {
		t.Errorf("revision turn = %+v", revision)
	}
	if endorsement.Endorse != "claude" || endorsement.Disposition != "" {
		t.Errorf("endorsement turn = %+v", endorsement)
	}
	if !slices.Equal(got.Notes, []string{"kimi dropped out"}) {
		t.Errorf("notes = %v", got.Notes)
	}
}

// The consensus payload is the winning proposal with the summary added and nothing
// else touched, so the apply path reads exactly the fields it always did.
func TestGrillConsensusPayload(t *testing.T) {
	payload, err := grillConsensusPayload(
		json.RawMessage(`{"disposition":"rewrite","proposed_description":"body","summary":"agreed"}`),
		grillDisagreement{Winner: "claude"},
	)
	if err != nil {
		t.Fatalf("grillConsensusPayload: %v", err)
	}
	outcome, ok := latestGrillOutcome([]hubstore.GrillMessage{{Kind: hubstore.GrillKindOutcome, Payload: payload}})
	if !ok {
		t.Fatal("the apply path could not read the consensus outcome")
	}
	if outcome.Disposition != "rewrite" || outcome.ProposedDescription != "body" || outcome.Summary != "agreed" {
		t.Fatalf("outcome = %+v, want the winning proposal unchanged", outcome)
	}
	if !strings.Contains(payload, `"disagreement"`) {
		t.Fatalf("payload carries no disagreement summary: %s", payload)
	}
}
