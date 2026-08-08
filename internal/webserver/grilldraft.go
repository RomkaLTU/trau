package webserver

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/sanitize"
)

// runDrafts is the second-opinion phase: once the interviewer has proposed, every
// challenger drafts its own outcome from the same transcript, concurrently, then the
// panel argues it out over bounded challenge rounds, and the session finishes on the
// consensus those rounds reached or on the side-by-side review they did not. It holds
// the session's one turn slot for the whole phase, so nothing else can spawn a child
// against the session while it runs. Panel runs are one-shot — nothing chains them, so
// no session id is recorded and every turn starts fresh.
func (r *grillRunner) runDrafts(sess hubstore.GrillSession) {
	ctx, cancel, ok := r.begin(sess.ID)
	if !ok {
		return
	}
	defer r.end(sess.ID, cancel)

	repo, found := r.srv.findRepoByRoot(sess.Repo)
	if !found {
		r.settle(sess.ID, hubstore.GrillParked, "the session's repository is no longer registered with the hub")
		return
	}
	cfg, err := r.srv.grillConfigFor(repo)
	if err != nil {
		r.settle(sess.ID, hubstore.GrillParked, "could not load the repository config: "+err.Error())
		return
	}
	prompt := r.srv.grillChallengerBrief(repo, cfg, sess)

	// A challenger that fails is dropped rather than aborting the phase, so one bad
	// draft never cancels another that is still working.
	var wg sync.WaitGroup
	for _, member := range sess.Challengers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.draftOne(ctx, sess, repo, cfg, member, prompt)
		}()
	}
	wg.Wait()
	r.finishPanel(sess, r.runChallengeRounds(ctx, sess, repo, cfg))
}

// draftOne runs a single challenger to completion. The spawn is the same headless
// shape as a first interviewer turn — same adapter, same working directory, the
// provider's own default model — pointed at the member's submit_decision surface
// instead of the session's own tools. Its reply is not streamed: two drafts running at
// once would interleave into one unreadable turn, so the panel gets an activity frame
// per member rather than their prose.
func (r *grillRunner) draftOne(
	ctx context.Context,
	sess hubstore.GrillSession,
	repo registry.Repo,
	cfg config.Config,
	member, prompt string,
) {
	adapter, ok := grillAdapterFor(r, member)
	if !ok {
		r.dropChallenger(sess.ID, member, "interviews do not yet support the "+member+" provider")
		return
	}
	if reason := grillProviderUnavailableReason(cfg, member, sess.Mode); reason != "" {
		r.dropChallenger(sess.ID, member, reason)
		return
	}
	endpoint := grillEndpoint{sid: sess.ID, member: member}
	spec, err := adapter.turnSpec(endpoint, repo, cfg, sess.Mode, grillModelDefault(cfg, member), "", prompt)
	if err != nil {
		r.dropChallenger(sess.ID, member, "could not prepare the draft run: "+err.Error())
		return
	}

	r.srv.publishGrillActivity(sess.ID, GrillActivityView{
		Kind:   grillActivityTool,
		ID:     grillDraftActivityID(member),
		Name:   member,
		Detail: member + " is drafting a second opinion",
	})
	out, runErr := r.spawnGrill(ctx, spec, func([]byte) {})
	_, resultErr := adapter.parseResult(out.stdout)

	drafted := r.srv.grillSubmittedInSession(sess.ID, member, 0)
	r.srv.publishGrillActivity(sess.ID, grillToolResult(grillDraftActivityID(member), drafted))
	if drafted {
		return
	}
	r.dropChallenger(sess.ID, member, grillDraftFailure(adapter, out, runErr, resultErr))
}

func grillDraftActivityID(member string) string { return "second-opinion-" + member }

// dropChallenger records a lost second opinion in the transcript. A draft that never
// lands is not a session failure — the review carries on with what did arrive — so the
// only trace it leaves is the note saying which provider dropped out and why.
func (r *grillRunner) dropChallenger(sid int64, member, reason string) {
	r.srv.appendGrillNotice(sid, member+" did not return a second opinion: "+reason)
}

// grillDraftFailure says in one line why a challenger produced nothing: a provider wall
// first, since that is the reason worth acting on, then the spawn's own error, and
// last the plain case of a run that ended without deciding anything.
func grillDraftFailure(adapter grillAdapter, out grillOutput, runErr error, resultErr bool) string {
	if reason := grillStallReason(adapter, out.stdout, out.stderr); reason != "" {
		return reason
	}
	if runErr != nil {
		return runErr.Error()
	}
	if resultErr {
		return "the draft run ended in an error"
	}
	return "the run ended without submitting a decision"
}

// finishPanel closes the phase. A consensus the challenge rounds reached is written as
// the session's canonical outcome, with the disagreement summary rolled into its
// payload, and the review then reads exactly as a solo one. Without a consensus the
// session finishes holding every proposal for the side-by-side review — except with a
// single proposal left, which degrades to the solo flow the way a session whose
// challengers all failed always has.
func (r *grillRunner) finishPanel(sess hubstore.GrillSession, contest grillChallengeResult) {
	// A stop or an abandon lands while the phase runs and settles the session by the
	// user's own hand. Whatever the phase collected is theirs to keep, but the phase
	// does not get to decide a session they have already put down.
	if latest, found, err := r.srv.stores.Grill().Session(sess.ID); err != nil || !found || latest.State != hubstore.GrillRunning {
		return
	}
	cycle, err := r.srv.grillPanelMessages(sess)
	if err != nil {
		r.settle(sess.ID, hubstore.GrillParked, "could not read the session's proposals: "+err.Error())
		return
	}
	if grillDecided(cycle) {
		return
	}
	proposals := grillCurrentProposals(cycle)
	if len(proposals) == 0 {
		r.settle(sess.ID, hubstore.GrillParked, grillNoOutcomeReason)
		return
	}
	if consensus, ok := grillConsensusProposal(proposals, contest); ok {
		r.settleConsensus(sess, cycle, consensus, contest)
	}
	finished, err := r.srv.stores.Grill().Transition(sess.ID, hubstore.GrillFinished, "")
	if err != nil {
		logger.Verbosef("grill %d: finish the panel: %v", sess.ID, err)
		return
	}
	r.srv.publishGrillState(finished)
}

// grillConsensusProposal picks the proposal the session settles on: the one the
// challenge rounds converged on, or the only one left when the panel came down to a
// single proposal. Anything else goes to the user side by side.
func grillConsensusProposal(proposals []GrillProposalView, contest grillChallengeResult) (GrillProposalView, bool) {
	if len(proposals) == 1 {
		return proposals[0], true
	}
	i := slices.IndexFunc(proposals, func(p GrillProposalView) bool { return p.Provider == contest.winner })
	if contest.winner == "" || i < 0 {
		return GrillProposalView{}, false
	}
	return proposals[i], true
}

// settleConsensus writes the winning proposal as the session's canonical outcome. A
// panel that actually contested the decision carries its disagreement summary into
// that payload; a sole surviving proposal is promoted verbatim, exactly as a session
// whose challengers all failed always has been.
func (r *grillRunner) settleConsensus(
	sess hubstore.GrillSession,
	cycle []hubstore.GrillMessage,
	consensus GrillProposalView,
	contest grillChallengeResult,
) {
	payload := string(consensus.Outcome)
	if contest.contested() {
		summary := grillDisagreementSummary(grillPanelTurns(cycle), consensus.Provider, contest.notes)
		merged, err := grillConsensusPayload(consensus.Outcome, summary)
		if err != nil {
			logger.Verbosef("grill %d: attach the disagreement summary: %v", sess.ID, err)
		} else {
			payload = merged
		}
	}
	outcome, _, err := r.srv.stores.Grill().AppendMessage(sess.ID, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleAgent,
		Kind:    hubstore.GrillKindOutcome,
		Payload: payload,
	})
	if err != nil {
		logger.Verbosef("grill %d: promote the consensus proposal: %v", sess.ID, err)
		return
	}
	r.srv.publishGrillMessage(outcome)
}

// grillSubmittedInSession reports whether provider has taken its turn in round, read
// fresh off the store: a panel turn arrives over the member's own MCP call, not
// through anything its run hands back.
func (s *Server) grillSubmittedInSession(sid int64, provider string, round int) bool {
	msgs, err := s.stores.Grill().Messages(sid, 0)
	if err != nil {
		return false
	}
	return grillSubmittedIn(grillActiveCycle(msgs), provider, round)
}

// grillPanelMessages reads the session's active panel cycle — everything after the
// latest reopen, which is what this phase's proposals and rounds belong to.
func (s *Server) grillPanelMessages(sess hubstore.GrillSession) ([]hubstore.GrillMessage, error) {
	msgs, err := s.stores.Grill().Messages(sess.ID, 0)
	if err != nil {
		return nil, err
	}
	return grillPanelCycle(sess, msgs), nil
}

// grillPanelContext is the material every panel prompt is built from: the issue the
// session is about, where its outcome would be written if the user approves it, and
// the whole interview transcript the proposals came out of. It is read once per phase
// — rendering the transcript is the expensive part — and shared by every member.
type grillPanelContext struct {
	repo        string
	issueID     string
	title       string
	description string
	destination string
	transcript  string
}

func (s *Server) grillPanelContext(repo registry.Repo, cfg config.Config, sess hubstore.GrillSession) grillPanelContext {
	title, description := "", ""
	if id := strings.TrimSpace(sess.IssueID); id != "" {
		if iss, found, err := s.stores.Issues().Get(repo.Root, id); err == nil && found {
			title, description = iss.Title, iss.Description
		}
	}
	return grillPanelContext{
		repo:        repo.Name,
		issueID:     sess.IssueID,
		title:       title,
		description: description,
		destination: grillOutcomeDestination(sess, cfg.EffectiveTrackerProvider()),
		transcript:  s.grillTranscript(sess.ID),
	}
}

// grillChallengerBrief composes the prompt every challenger drafts from. All of them
// read the same brief: they are giving independent opinions on one interview, so
// nothing about it is per-member.
func (s *Server) grillChallengerBrief(repo registry.Repo, cfg config.Config, sess hubstore.GrillSession) string {
	return s.grillPanelPrompt(sess, grillChallengerPrompt(s.grillPanelContext(repo, cfg, sess)))
}

// grillChallengeBrief composes the shared body of one challenge round: the same
// interview, the proposals every member currently stands behind, and every challenge
// note the rounds have raised. Only the seat differs per member, which the spawn
// prepends.
func (s *Server) grillChallengeBrief(
	sess hubstore.GrillSession,
	base grillPanelContext,
	cycle []hubstore.GrillMessage,
	round, rounds int,
) string {
	prompt := grillChallengePrompt(base, grillCurrentProposals(cycle), grillChallengeNotes(cycle), round, rounds)
	return s.grillPanelPrompt(sess, prompt)
}

func grillChallengeNotes(cycle []hubstore.GrillMessage) []grillChallengeNote {
	out := []grillChallengeNote{}
	for _, t := range grillPanelTurns(cycle) {
		if note := strings.TrimSpace(t.ChallengeNote); note != "" {
			out = append(out, grillChallengeNote{provider: t.Provider, round: t.Round, note: note})
		}
	}
	return out
}

// grillPanelPrompt scrubs a composed panel prompt the way every grilling prompt is
// scrubbed before it reaches a child.
func (s *Server) grillPanelPrompt(sess hubstore.GrillSession, prompt string) string {
	prompt, scrubbed := sanitize.PromptText(prompt)
	if scrubbed {
		logger.Printf("grill %d: panel brief contained a raw NUL byte — replaced with its visible escape", sess.ID)
	}
	return prompt
}

// grillOutcomeDestination says where an approved decision would be written, so a
// challenger weighs its disposition against a real place rather than an abstraction.
func grillOutcomeDestination(sess hubstore.GrillSession, tracker string) string {
	where := strings.TrimSpace(tracker)
	if where == "" || where == grillDestInternal {
		where = "the hub's internal issue store"
	}
	if id := strings.TrimSpace(sess.IssueID); id != "" {
		return id + " on " + where
	}
	return "a new issue on " + where
}

// grillTranscript renders the interview as a challenger reads it: every question,
// round and answer in the order they happened, plus the notes the user opened on and
// the interjections they steered with. Hub bookkeeping — a model switch, a dropped
// challenger — is left out: it says nothing about the work.
func (s *Server) grillTranscript(sid int64) string {
	msgs, err := s.stores.Grill().Messages(sid, 0)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, m := range msgs {
		switch {
		case m.Kind == hubstore.GrillKindInfo && m.Role == hubstore.GrillRoleUser:
			fmt.Fprintf(&b, "\nUser opened with: %s\n", grillMessageText(m.Payload))
		case m.Kind == hubstore.GrillKindQuestion:
			if round, ok := grillRoundQuestions(m.Payload); ok {
				b.WriteString("\nInterviewer asked a round:\n")
				s.writeGrillRound(&b, m.ID, round)
				continue
			}
			fmt.Fprintf(&b, "\nInterviewer asked: %s\n", grillMessageText(m.Payload))
		case m.Kind == hubstore.GrillKindAnswer:
			if text := grillMessageText(m.Payload); text != "" {
				fmt.Fprintf(&b, "User answered: %s\n", text)
			}
		case m.Kind == hubstore.GrillKindInterjection:
			fmt.Fprintf(&b, "User interjected: %s\n", grillMessageText(m.Payload))
		}
	}
	return strings.TrimSpace(b.String())
}

// writeGrillRound lists a round's questions with the answers they collected, keyed by
// the position each question holds — the pairing the answers are stored under.
func (s *Server) writeGrillRound(b *strings.Builder, messageID int64, round []grillRoundQuestion) {
	answers, err := s.stores.Grill().RoundAnswers(messageID)
	if err != nil {
		logger.Verbosef("grill message %d: read round answers: %v", messageID, err)
	}
	byIndex := make(map[int]string, len(answers))
	for _, a := range answers {
		byIndex[a.Index] = a.Text
	}
	for i, q := range round {
		fmt.Fprintf(b, "  %d. %s\n", i+1, strings.TrimSpace(q.Text))
		if answer := strings.TrimSpace(byIndex[i]); answer != "" {
			fmt.Fprintf(b, "     User answered: %s\n", answer)
		} else {
			b.WriteString("     (unanswered)\n")
		}
	}
}
