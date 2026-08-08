package webserver

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

// grillChallengeResult is what the challenge rounds settled: the provider whose
// proposal the panel converged on, empty when it never did, and the notes the rounds
// could not record as a member's own turn — a member that dropped out, a panel that
// came down to one survivor. rounds counts the rounds that actually ran, so a session
// that skipped them is told apart from one that ran them and disagreed.
type grillChallengeResult struct {
	winner string
	notes  []string
	rounds int
}

// contested reports whether the rounds ran at all: only a consensus they reached
// carries a disagreement summary.
func (c grillChallengeResult) contested() bool { return c.rounds > 0 }

// runChallengeRounds is the convergence phase: for each round every remaining panel
// member sees the competing proposals and the challenge notes so far, and either
// endorses one proposal or revises its own with a note saying what it disputes.
// Unanimity stops the rounds early and settles the session on one consensus proposal;
// anything else after the last round falls back to the side-by-side review.
//
// Every turn is a fresh headless run, symmetric for every member: the interviewer's
// interactive session chain is deliberately not resumed, so its verdict is decided
// from the same material and against the same contract as everybody else's. The phase
// runs inside the turn slot the draft phase already holds.
func (r *grillRunner) runChallengeRounds(
	ctx context.Context,
	sess hubstore.GrillSession,
	repo registry.Repo,
	cfg config.Config,
) grillChallengeResult {
	out := grillChallengeResult{notes: []string{}}
	if cfg.GrillChallengeRounds < 1 {
		return out
	}
	cycle, err := r.srv.grillPanelMessages(sess)
	if err != nil {
		return out
	}
	// A member with nothing on the record has nothing to defend and no proposal to
	// endorse against: a panel of one is the drafts' own degrade, not a contest.
	members := grillPanelHolders(sess, cycle)
	if len(members) < 2 {
		return out
	}

	base := r.srv.grillPanelContext(repo, cfg, sess)
	for round := 1; round <= cfg.GrillChallengeRounds; round++ {
		if ctx.Err() != nil {
			return out
		}
		members = r.runOneChallengeRound(ctx, sess, repo, cfg, base, cycle, members, round, &out)
		out.rounds = round
		if len(members) == 0 {
			out.notes = append(out.notes, "every panel member dropped out of the challenge rounds")
			return out
		}
		if len(members) == 1 {
			out.winner = members[0]
			out.notes = append(out.notes,
				members[0]+" is the only panel member left, so its proposal is the consensus by default")
			return out
		}
		cycle, err = r.srv.grillPanelMessages(sess)
		if err != nil {
			return out
		}
		if winner, ok := grillConsensus(members, grillRoundVotes(cycle, round)); ok {
			out.winner = winner
			return out
		}
	}
	return out
}

// runOneChallengeRound spawns every remaining member's turn concurrently and returns
// the members that came back with one. A member that fails is dropped with a visible
// note rather than aborting the round, so unanimity is computed over the rest.
func (r *grillRunner) runOneChallengeRound(
	ctx context.Context,
	sess hubstore.GrillSession,
	repo registry.Repo,
	cfg config.Config,
	base grillPanelContext,
	cycle []hubstore.GrillMessage,
	members []string,
	round int,
	out *grillChallengeResult,
) []string {
	shared := r.srv.grillChallengeBrief(sess, base, cycle, round, cfg.GrillChallengeRounds)

	drops := make([]string, len(members))
	var wg sync.WaitGroup
	for i, member := range members {
		wg.Add(1)
		go func() {
			defer wg.Done()
			drops[i] = r.challengeOne(ctx, sess, repo, cfg, member, round, grillChallengeMemberPrompt(member, shared))
		}()
	}
	wg.Wait()

	kept := make([]string, 0, len(members))
	for i, member := range members {
		if drops[i] == "" {
			kept = append(kept, member)
			continue
		}
		note := fmt.Sprintf("%s dropped out of challenge round %d: %s", member, round, drops[i])
		r.srv.appendGrillNotice(sess.ID, note)
		out.notes = append(out.notes, note)
	}
	return kept
}

// challengeOne runs one member's turn in one round and reports why it produced
// nothing, or the empty string when its verdict landed. The spawn is the draft
// phase's shape exactly — same adapter, same working directory, the provider's own
// default model, no resumed chain — pointed at the member's submit_decision surface
// for this round.
func (r *grillRunner) challengeOne(
	ctx context.Context,
	sess hubstore.GrillSession,
	repo registry.Repo,
	cfg config.Config,
	member string,
	round int,
	prompt string,
) string {
	adapter, ok := grillAdapterFor(r, member)
	if !ok {
		return "interviews do not yet support the " + member + " provider"
	}
	if reason := grillProviderUnavailableReason(cfg, member, sess.Mode); reason != "" {
		return reason
	}
	endpoint := grillEndpoint{sid: sess.ID, member: member, round: round}
	spec, err := adapter.turnSpec(endpoint, repo, cfg, sess.Mode, grillModelDefault(cfg, member), "", prompt)
	if err != nil {
		return "could not prepare the challenge run: " + err.Error()
	}

	id := grillChallengeActivityID(member, round)
	r.srv.publishGrillActivity(sess.ID, GrillActivityView{
		Kind:   grillActivityTool,
		ID:     id,
		Name:   member,
		Detail: fmt.Sprintf("%s is weighing the proposals in challenge round %d", member, round),
	})
	out, runErr := r.spawnGrill(ctx, spec, func([]byte) {})
	_, resultErr := adapter.parseResult(out.stdout)

	spoke := r.srv.grillSubmittedInSession(sess.ID, member, round)
	r.srv.publishGrillActivity(sess.ID, grillToolResult(id, spoke))
	if spoke {
		return ""
	}
	return grillDraftFailure(adapter, out, runErr, resultErr)
}

func grillChallengeActivityID(member string, round int) string {
	return "challenge-" + strconv.Itoa(round) + "-" + member
}

// grillPanelHolders names the panel members holding a proposal, in panel order: the
// interviewer first, then the challengers whose drafts landed.
func grillPanelHolders(sess hubstore.GrillSession, cycle []hubstore.GrillMessage) []string {
	proposals := grillCurrentProposals(cycle)
	out := make([]string, 0, len(proposals))
	for _, member := range grillPanel(sess) {
		if slices.ContainsFunc(proposals, func(p GrillProposalView) bool { return p.Provider == member }) {
			out = append(out, member)
		}
	}
	return out
}
