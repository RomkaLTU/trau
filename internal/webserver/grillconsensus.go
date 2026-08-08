package webserver

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

// grillRoundVote is one panel member's disposition in a challenge round: the member
// that spoke, and the provider whose proposal it adopts. An empty Endorse is a
// revision, which implicitly endorses the member's own current proposal.
type grillRoundVote struct {
	Provider string
	Endorse  string
}

// grillConsensus resolves one challenge round over the members still on the panel.
// The panel converges only on unanimity — every remaining member endorsing the same
// proposal, with a proposal's author counting as endorsing its own — because with two
// or three members a majority is one vote and outcomes are not comparable enough for
// a vote to mean anything (ADR 0043). A member that said nothing keeps the round open
// rather than being read as agreement, and an endorsement of a provider no longer on
// the panel resolves nothing: the proposal it names has left the contest.
func grillConsensus(members []string, votes []grillRoundVote) (string, bool) {
	if len(members) == 0 {
		return "", false
	}
	endorsed := make(map[string]string, len(votes))
	for _, v := range votes {
		endorsed[v.Provider] = v.Endorse
	}
	winner := ""
	for _, member := range members {
		vote, spoke := endorsed[member]
		if !spoke {
			return "", false
		}
		if vote == "" {
			vote = member
		}
		if !slices.Contains(members, vote) {
			return "", false
		}
		if winner != "" && vote != winner {
			return "", false
		}
		winner = vote
	}
	return winner, true
}

// grillPanelTurns reads every panel turn a conversation recorded — the draft
// proposals and the endorsements and revisions the challenge rounds added — in the
// order they landed. A row whose payload will not parse is dropped rather than
// failing the read: the review still has the turns that did.
func grillPanelTurns(msgs []hubstore.GrillMessage) []GrillProposalView {
	out := []GrillProposalView{}
	for _, m := range msgs {
		if m.Kind != hubstore.GrillKindProposal {
			continue
		}
		var p grillProposal
		if json.Unmarshal([]byte(m.Payload), &p) != nil {
			continue
		}
		if len(p.Outcome) == 0 && p.Endorse == "" {
			continue
		}
		out = append(out, GrillProposalView{
			MessageID:     strconv.FormatInt(m.ID, 10),
			Provider:      p.Provider,
			Round:         p.Round,
			Outcome:       p.Outcome,
			Endorse:       p.Endorse,
			ChallengeNote: p.ChallengeNote,
		})
	}
	return out
}

// grillCurrentProposals is the proposal each panel member currently stands behind:
// its latest revision, in the order the members first proposed. A member that
// endorsed somebody else keeps its own proposal on the record — the endorsement moves
// its vote, not its draft — so the side-by-side review still shows what it drafted.
func grillCurrentProposals(msgs []hubstore.GrillMessage) []GrillProposalView {
	out := []GrillProposalView{}
	for _, p := range grillProposalViews(msgs) {
		if i := slices.IndexFunc(out, func(c GrillProposalView) bool { return c.Provider == p.Provider }); i >= 0 {
			out[i] = p
			continue
		}
		out = append(out, p)
	}
	return out
}

// grillRoundVotes reads how each member disposed of round in the active cycle.
func grillRoundVotes(msgs []hubstore.GrillMessage, round int) []grillRoundVote {
	out := []grillRoundVote{}
	for _, t := range grillPanelTurns(msgs) {
		if t.Round != round {
			continue
		}
		out = append(out, grillRoundVote{Provider: t.Provider, Endorse: t.Endorse})
	}
	return out
}

// grillActiveCycle narrows a conversation to the panel cycle running now. A finished
// session that takes a follow-up answer reopens and runs the whole cycle again —
// drafts and challenge rounds — so the rows the earlier cycles left behind stay in the
// transcript as history and settle nothing. The boundary is the latest user answer
// that follows a proposal or an outcome, which is exactly the answer that reopened the
// session; an interjection is not one, since it steers a cycle already in flight.
func grillActiveCycle(msgs []hubstore.GrillMessage) []hubstore.GrillMessage {
	start, proposed := 0, false
	for i, m := range msgs {
		switch {
		case m.Kind == hubstore.GrillKindProposal || m.Kind == hubstore.GrillKindOutcome:
			proposed = true
		case proposed && m.Role == hubstore.GrillRoleUser && m.Kind == hubstore.GrillKindAnswer:
			start, proposed = i, false
		}
	}
	return msgs[start:]
}

// grillPanelCycle scopes a conversation to the active cycle for a session that has a
// panel, and leaves a solo session's own whole transcript alone: nothing there is
// superseded by a reopen, since a solo interview writes its outcome directly.
func grillPanelCycle(sess hubstore.GrillSession, msgs []hubstore.GrillMessage) []hubstore.GrillMessage {
	if len(sess.Challengers) == 0 {
		return msgs
	}
	return grillActiveCycle(msgs)
}

// grillPanel names the whole panel a second-opinion session runs: the interviewer's
// own provider first, then the challengers it was created with. Every member decides
// against the same contract, so the interviewer is a member of the contest its
// proposal opened rather than its chair.
func grillPanel(sess hubstore.GrillSession) []string {
	panel := make([]string, 0, len(sess.Challengers)+1)
	panel = append(panel, grillEffectiveProvider(sess.Provider))
	return append(panel, sess.Challengers...)
}

// grillDisagreement is the mechanical record of how a panel reached its consensus —
// no extra model run, only what the rounds already wrote down. It rides in the
// consensus outcome's payload so the review can show what the members disagreed about
// before they settled, which a single agreed proposal otherwise hides.
type grillDisagreement struct {
	Winner  string                   `json:"winner,omitempty"`
	Initial []grillDisposition       `json:"initial"`
	Rounds  []grillDisagreementRound `json:"rounds"`
	Notes   []string                 `json:"notes,omitempty"`
}

// grillDisposition is one member's opening position: the disposition it drafted.
type grillDisposition struct {
	Provider    string `json:"provider"`
	Disposition string `json:"disposition"`
}

type grillDisagreementRound struct {
	Round int                     `json:"round"`
	Turns []grillDisagreementTurn `json:"turns"`
}

// grillDisagreementTurn is one member's move in one round: the proposal it endorsed,
// or the disposition it revised to and the note saying what it disputes.
type grillDisagreementTurn struct {
	Provider    string `json:"provider"`
	Endorse     string `json:"endorse,omitempty"`
	Disposition string `json:"disposition,omitempty"`
	Note        string `json:"note,omitempty"`
}

// grillDisagreementSummary assembles the summary from the turns the panel recorded:
// each member's initial disposition, then every round's endorsements and revisions in
// order. notes carry what the rounds could not record as a turn — a member that
// dropped out, a panel that came down to one survivor.
func grillDisagreementSummary(turns []GrillProposalView, winner string, notes []string) grillDisagreement {
	out := grillDisagreement{
		Winner:  winner,
		Initial: []grillDisposition{},
		Rounds:  []grillDisagreementRound{},
		Notes:   notes,
	}
	for _, t := range turns {
		if t.Round == 0 {
			out.Initial = append(out.Initial, grillDisposition{
				Provider:    t.Provider,
				Disposition: grillOutcomeDisposition(t.Outcome),
			})
			continue
		}
		turn := grillDisagreementTurn{
			Provider: t.Provider,
			Endorse:  t.Endorse,
			Note:     t.ChallengeNote,
		}
		if t.Endorse == "" {
			turn.Disposition = grillOutcomeDisposition(t.Outcome)
		}
		i := slices.IndexFunc(out.Rounds, func(r grillDisagreementRound) bool { return r.Round == t.Round })
		if i < 0 {
			out.Rounds = append(out.Rounds, grillDisagreementRound{Round: t.Round, Turns: []grillDisagreementTurn{turn}})
			continue
		}
		out.Rounds[i].Turns = append(out.Rounds[i].Turns, turn)
	}
	return out
}

// grillConsensusPayload splices the disagreement summary into the winning proposal's
// own payload. The canonical outcome stays a single kind=outcome row of exactly the
// shape the apply path already reads — the summary is one more key it ignores — so an
// apply of a consensus proposal is a solo apply of that payload.
func grillConsensusPayload(outcome json.RawMessage, summary grillDisagreement) (string, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(outcome, &fields); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		return "", err
	}
	fields["disagreement"] = encoded
	merged, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	return string(merged), nil
}

// grillOutcomeDisposition reads a proposal payload's disposition alone, which is all
// the summary says about a position the review shows in full elsewhere.
func grillOutcomeDisposition(outcome json.RawMessage) string {
	var o struct {
		Disposition string `json:"disposition"`
	}
	if json.Unmarshal(outcome, &o) != nil {
		return ""
	}
	return strings.TrimSpace(o.Disposition)
}
