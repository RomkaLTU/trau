package webserver

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RomkaLTU/trau/internal/attachfile"
	"github.com/RomkaLTU/trau/internal/prompts"
)

// grillPromptInput is the issue context a first-turn prompt is built from: the issue
// the session is anchored to with its attachments materialized locally, the focus note
// it was opened with, and whether the agent's recommendations are answered for it.
type grillPromptInput struct {
	issueID     string
	title       string
	description string
	focus       string
	files       []attachfile.File
	autoAccept  bool
}

// grillIssuePrompt is the first-turn prompt for grilling an existing issue: the
// agent interviews the user round by round via the ask_round MCP tool and
// ends with a finish_session proposal. It runs with the repo as cwd, so it is told
// to read the code before asking when that sharpens a question. Resume turns carry
// only the user's answer — the child already holds this context.
func grillIssuePrompt(r prompts.Renderer, in grillPromptInput) string {
	return r.Render("grill_issue", grillIssueData(in))
}

// grillPregrillPrompt is the first-turn prompt for an AFK pre-grill pass: no user is
// present, so the agent reads the repo and interviews against its own recommendations,
// which the session auto-accepts. It finishes with a rewrite or no_change, or lodges
// the one question it would not recommend an answer to via ask_user — which parks at
// once — and ends its turn. The parked question waits for a live session later.
func grillPregrillPrompt(r prompts.Renderer, in grillPromptInput) string {
	return r.Render("grill_pregrill", grillIssueData(in))
}

// grillAuthoringPrompt is the first-turn prompt for a session anchored to the repo
// alone (no issue): the from-scratch authoring flow. The agent interviews the user
// toward a fully-specified new issue and ends with a create proposal — a single
// issue or an epic with sub-issues. idea is the one-line seed the user started with;
// it is empty when they opened the session without one.
func grillAuthoringPrompt(r prompts.Renderer, idea string, autoAccept bool) string {
	return r.Render("grill_authoring", prompts.GrillAuthoringData{
		Idea:       strings.TrimSpace(idea),
		AutoAccept: autoAccept,
	})
}

// grillAuthoringFromReportPrompt is the first-turn prompt for a draft started from a
// finished research report: the report's whole findings ride in the prompt — never in
// the visible thread — so the agent opens on what the investigation left undecided,
// the scope and product choices, rather than re-asking what it already settled.
func grillAuthoringFromReportPrompt(r prompts.Renderer, report grillReport, autoAccept bool) string {
	return r.Render("grill_authoring_from_report", prompts.GrillReportData{
		Report:     report.title,
		Findings:   report.findings,
		Sources:    grillReportSourceSection(report.sources),
		AutoAccept: autoAccept,
	})
}

// grillResearchPrompt is the first-turn prompt for a research session anchored to an
// issue: the agent answers the question against primary sources — the web and the
// repo — with the issue as its context, and finishes with a findings report rather
// than an issue rewrite. The focus note aims the research at the issue.
func grillResearchPrompt(r prompts.Renderer, in grillPromptInput) string {
	return r.Render("grill_research", prompts.GrillResearchData{GrillIssueData: grillIssueData(in)})
}

// grillFixPrompt is the first-turn prompt for a fix session: the agent diagnoses why
// the ticket's last attempt failed from the dossier compiled at create, inspects the
// WIP branch the attempt was left on, and finishes with a rewrite carrying the
// diagnosis and the instructions the next attempt must follow.
func grillFixPrompt(r prompts.Renderer, in grillPromptInput, run grillFailedRun) string {
	return r.Render("grill_fix", prompts.GrillFixData{
		GrillIssueData: grillIssueData(in),
		Failure:        grillFailureLine(run),
		Dossier:        grillDossierPath(run.Ticket),
		Branch:         run.Branch,
	})
}

// grillFailureLine states how the attempt ended in one line: the class it was filed
// under and, when the run recorded one, the reason behind it. A checkpoint cleared
// since the session opened leaves the dossier as the only record of the run, which
// is worth saying rather than rendering an empty class.
func grillFailureLine(run grillFailedRun) string {
	if run.FailureClass == "" {
		return "recorded in the dossier — the run's checkpoint has since been cleared"
	}
	line := run.FailureClass + " at phase " + run.Phase
	if run.FailureReason != "" {
		line += " — " + run.FailureReason
	}
	return line
}

// grillFixOpeningNote is the line a fix session opens the conversation on: the
// surface starts it from a button rather than a typed idea, so the opener states what
// the session is for and why the run failed.
func grillFixOpeningNote(run grillFailedRun) string {
	note := "Propose a fix for " + run.Ticket + ": "
	if reason := strings.TrimSpace(run.FailureReason); reason != "" {
		return note + reason
	}
	return note + run.FailureClass
}

// grillResearchIdeaPrompt is the from-scratch counterpart: nothing anchors the
// session, so the opening note is the question to answer; it is empty when the user
// opened the session without one.
func grillResearchIdeaPrompt(r prompts.Renderer, question string, autoAccept bool) string {
	return r.Render("grill_research", prompts.GrillResearchData{
		GrillIssueData: prompts.GrillIssueData{AutoAccept: autoAccept},
		Idea:           strings.TrimSpace(question),
	})
}

// grillChallengerPrompt is the draft-phase prompt: it hands a challenger the finished
// interview and asks it to decide the outcome for itself. It is hardcoded here beside
// the other grilling prompts rather than carried in the prompt catalog, because it has
// to state the submit_decision contract verbatim — a challenger that drifts from
// finish_session's disposition rules drafts a proposal the review cannot place beside
// the interviewer's.
func grillChallengerPrompt(in grillPanelContext) string {
	var b strings.Builder
	b.WriteString(`You are giving a second opinion on a triage interview that has just finished.

Another agent interviewed the user about the work below and has proposed an outcome. You
do not see that proposal, and you must not try to guess it: decide for yourself, from the
same material, what the outcome should be. The user will read your decision beside the
other proposals and pick one.

`)
	grillWritePanelContext(&b, in)
	b.WriteString(grillChallengerRules)
	return b.String()
}

// grillWritePanelContext writes the material every panel prompt opens on: the issue,
// where an approved decision would be written, and the interview it came out of.
func grillWritePanelContext(b *strings.Builder, in grillPanelContext) {
	b.WriteString("Repository: " + in.repo + "\n")
	if id := strings.TrimSpace(in.issueID); id != "" {
		b.WriteString("Issue: " + id + "\n")
	} else {
		b.WriteString("Issue: none — this session drafts a new one\n")
	}
	if title := strings.TrimSpace(in.title); title != "" {
		b.WriteString("Title: " + title + "\n")
	}
	b.WriteString("Destination: " + in.destination + "\n")
	b.WriteString("\nIssue description:\n")
	if desc := strings.TrimSpace(in.description); desc != "" {
		b.WriteString(desc + "\n")
	} else {
		b.WriteString("(no description yet)\n")
	}
	b.WriteString("\nInterview transcript, in order:\n")
	if transcript := strings.TrimSpace(in.transcript); transcript != "" {
		b.WriteString(transcript + "\n")
	} else {
		b.WriteString("(the interview recorded no questions or answers)\n")
	}
}

// grillChallengerRules restates finish_session's dispositions and their required
// fields, so a second opinion is decided against the same contract as the proposal it
// contests. Keep it in step with grillDecisionSchema.
const grillChallengerRules = `
You have the repository checked out at your working directory. Read whatever code you
need to judge the work; do not ask the user anything — they are not there, and you have
no tool that reaches them.

Decide one outcome and submit it with exactly one call to the submit_decision tool, then
end your turn. The dispositions are:

- "rewrite" — replace the issue description. Requires proposed_description: the full
  replacement body.
- "split" — the issue is epic-shaped. Requires proposed_description framing the epic goal
  and a non-empty sub_issues breakdown, each entry a thin vertical slice that is
  end-to-end and independently verifiable on its own. A layer ("schema", "backend",
  "UI") is not a slice.
- "needs_split" — too large to slice confidently; flag it for splitting and nothing more.
- "create" — author a brand-new issue. Requires title and proposed_description; add a
  sub_issues breakdown to file it as an epic instead of a single issue.
- "research" — what this session produced is a report, not an issue body. Requires title
  and findings, the complete Markdown report.
- "no_change" — nothing needs writing.

summary is required on every disposition: state the clarifications the interview reached
and why they lead to the outcome you chose. Disagreeing with the direction the interview
took is a legitimate second opinion — say so in the summary and decide accordingly.
Nothing is written to the tracker until the user approves.
`

// grillChallengeMemberPrompt names the member a shared round brief is spawned for.
// Every member reads the same proposals and the same notes; only the seat differs, and
// a member that does not know its own seat cannot tell its proposal from the ones it
// is being asked to weigh.
func grillChallengeMemberPrompt(member, shared string) string {
	return "You are the panel member \"" + member + "\". The proposal labelled " + member +
		" below is your own.\n\n" + shared
}

// grillChallengePrompt is the challenge-round prompt: it hands every panel member the
// same interview, the proposals each member currently stands behind, and every
// challenge note raised so far, then asks for one verdict — endorse a proposal, or
// revise with a note saying what it disputes. It states the submit_decision contract
// verbatim for the same reason the draft prompt does: a member that drifts from the
// dispositions writes a revision the review cannot place beside the others.
func grillChallengePrompt(
	in grillPanelContext,
	proposals []GrillProposalView,
	notes []grillChallengeNote,
	round, rounds int,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, `This is challenge round %d of %d on a triage interview that has already finished.

Every member of this panel drafted an outcome from the same interview. You now see all of
them. Either adopt the one you judge best, or stand your ground with a revision of your
own and say what you dispute. The panel settles the session itself when every member
endorses the same proposal; otherwise the user reviews the surviving proposals side by
side.

`, round, rounds)
	grillWritePanelContext(&b, in)
	b.WriteString("\nThe proposals on the table:\n")
	for _, p := range proposals {
		grillWriteProposal(&b, p)
	}
	if len(notes) > 0 {
		b.WriteString("\nChallenge notes raised so far:\n")
		for _, n := range notes {
			fmt.Fprintf(&b, "  - %s, round %d: %s\n", n.provider, n.round, strings.TrimSpace(n.note))
		}
	}
	b.WriteString(grillChallengeRules)
	return b.String()
}

// grillChallengeNote is one challenge note as a later round reads it back.
type grillChallengeNote struct {
	provider string
	round    int
	note     string
}

// grillWriteProposal renders one proposal as a member reads it: the disposition and
// the fields that disposition decides with, in full. A member endorsing a proposal
// adopts it as written, so nothing here is abbreviated.
func grillWriteProposal(b *strings.Builder, p GrillProposalView) {
	var o grillOutcome
	if json.Unmarshal(p.Outcome, &o) != nil {
		return
	}
	fmt.Fprintf(b, "\n--- Proposal by %s — disposition %q\n", p.Provider, o.Disposition)
	fmt.Fprintf(b, "Summary: %s\n", strings.TrimSpace(o.Summary))
	if title := strings.TrimSpace(o.Title); title != "" {
		fmt.Fprintf(b, "Title: %s\n", title)
	}
	if desc := strings.TrimSpace(o.ProposedDescription); desc != "" {
		fmt.Fprintf(b, "Proposed description:\n%s\n", desc)
	}
	if findings := strings.TrimSpace(o.Findings); findings != "" {
		fmt.Fprintf(b, "Findings:\n%s\n", findings)
	}
	for i, sub := range o.SubIssues {
		fmt.Fprintf(b, "Sub-issue %d: %s\n%s\n", i+1, strings.TrimSpace(sub.Title), strings.TrimSpace(sub.Description))
	}
}

// grillChallengeRules states the round's own contract on top of the dispositions every
// panel decision shares. Keep it in step with grillMemberDecisionSchema.
const grillChallengeRules = `
You have the repository checked out at your working directory. Read whatever code you
need to judge the proposals; do not ask the user anything — they are not there, and you
have no tool that reaches them.

Submit exactly one call to the submit_decision tool, then end your turn. It takes one of
two shapes:

- Endorse: pass endorse with the provider name of the proposal you adopt as-is, and
  nothing else. Endorsing your own proposal is how you stand by it unchanged.
- Revise: pass a complete decision of your own — the same fields a first draft takes —
  plus challenge_note saying what you dispute in the proposals you did not endorse. A
  revision keeps you behind your own proposal.

Endorse when the difference no longer matters to the work. Hold your ground when it does:
converging on a proposal you believe is wrong is worse than the user reading both.

The dispositions a revision decides with are:

- "rewrite" — replace the issue description. Requires proposed_description: the full
  replacement body.
- "split" — the issue is epic-shaped. Requires proposed_description framing the epic goal
  and a non-empty sub_issues breakdown, each entry a thin vertical slice that is
  end-to-end and independently verifiable on its own. A layer ("schema", "backend",
  "UI") is not a slice.
- "needs_split" — too large to slice confidently; flag it for splitting and nothing more.
- "create" — author a brand-new issue. Requires title and proposed_description; add a
  sub_issues breakdown to file it as an epic instead of a single issue.
- "research" — what this session produced is a report, not an issue body. Requires title
  and findings, the complete Markdown report.
- "no_change" — nothing needs writing.

summary is required on a revision: state what you changed and why. Nothing is written to
the tracker until the user approves.
`

func grillIssueData(in grillPromptInput) prompts.GrillIssueData {
	return prompts.GrillIssueData{
		ID:          in.issueID,
		Title:       strings.TrimSpace(in.title),
		Body:        grillIssueBody(in.description, in.files),
		Attachments: attachfile.Section(in.files),
		Focus:       strings.TrimSpace(in.focus),
		AutoAccept:  in.autoAccept,
	}
}

// grillIssueBody renders the description with every reference to one of the
// issue's images repointed at the local copy the session materialized — so the
// interviewing agent can open a screenshot the ticket only linked to.
func grillIssueBody(description string, files []attachfile.File) string {
	if d := strings.TrimSpace(description); d != "" {
		return attachfile.Rewrite(d, files)
	}
	return "(no description yet)"
}
