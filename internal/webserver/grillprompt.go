package webserver

import (
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

// grillChallengerInput is what one second-opinion draft is decided from: the issue the
// session is about, where its outcome would be written if the user approves it, and
// the whole interview transcript the interviewer's own proposal came out of.
type grillChallengerInput struct {
	repo        string
	issueID     string
	title       string
	description string
	destination string
	transcript  string
}

// grillChallengerPrompt is the draft-phase prompt: it hands a challenger the finished
// interview and asks it to decide the outcome for itself. It is hardcoded here beside
// the other grilling prompts rather than carried in the prompt catalog, because it has
// to state the submit_decision contract verbatim — a challenger that drifts from
// finish_session's disposition rules drafts a proposal the review cannot place beside
// the interviewer's.
func grillChallengerPrompt(in grillChallengerInput) string {
	var b strings.Builder
	b.WriteString(`You are giving a second opinion on a triage interview that has just finished.

Another agent interviewed the user about the work below and has proposed an outcome. You
do not see that proposal, and you must not try to guess it: decide for yourself, from the
same material, what the outcome should be. The user will read your decision beside the
other proposals and pick one.

`)
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
	b.WriteString(grillChallengerRules)
	return b.String()
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
