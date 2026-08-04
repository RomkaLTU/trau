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
// agent interviews the user one question at a time via the ask_user MCP tool and
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
