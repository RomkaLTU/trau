package webserver

import (
	"strings"

	"github.com/RomkaLTU/trau/internal/attachfile"
	"github.com/RomkaLTU/trau/internal/prompts"
)

// grillIssuePrompt is the first-turn prompt for grilling an existing issue: the
// agent interviews the user one question at a time via the ask_user MCP tool and
// ends with a finish_session proposal. It runs with the repo as cwd, so it is told
// to read the code before asking when that sharpens a question. focus is the note
// the session was opened with, and aims the opening question. Resume turns carry
// only the user's answer — the child already holds this context.
func grillIssuePrompt(r prompts.Renderer, issueID, title, description, focus string, files []attachfile.File) string {
	data := grillIssueData(issueID, title, description, files)
	data.Focus = strings.TrimSpace(focus)
	return r.Render("grill_issue", data)
}

// grillPregrillPrompt is the first-turn prompt for an AFK pre-grill pass: no user
// is present, so the agent reads the repo and either finishes with a rewrite or
// no_change, or lodges its single opening question via ask_user — which parks at
// once — and ends its turn. The parked question waits for a live session later.
func grillPregrillPrompt(r prompts.Renderer, issueID, title, description string, files []attachfile.File) string {
	return r.Render("grill_pregrill", grillIssueData(issueID, title, description, files))
}

// grillAuthoringPrompt is the first-turn prompt for a session anchored to the repo
// alone (no issue): the from-scratch authoring flow. The agent interviews the user
// toward a fully-specified new issue and ends with a create proposal — a single
// issue or an epic with sub-issues. idea is the one-line seed the user started with;
// it is empty when they opened the session without one.
func grillAuthoringPrompt(r prompts.Renderer, idea string) string {
	return r.Render("grill_authoring", prompts.GrillAuthoringData{Idea: strings.TrimSpace(idea)})
}

// grillResearchPrompt is the first-turn prompt for a research session anchored to an
// issue: the agent answers the question against primary sources — the web and the
// repo — with the issue as its context, and finishes with a findings report rather
// than an issue rewrite. focus is the note the session was opened with, and aims the
// research at the issue.
func grillResearchPrompt(r prompts.Renderer, issueID, title, description, focus string, files []attachfile.File) string {
	issue := grillIssueData(issueID, title, description, files)
	issue.Focus = strings.TrimSpace(focus)
	return r.Render("grill_research", prompts.GrillResearchData{GrillIssueData: issue})
}

// grillResearchIdeaPrompt is the from-scratch counterpart: nothing anchors the
// session, so the opening note is the question to answer; it is empty when the user
// opened the session without one.
func grillResearchIdeaPrompt(r prompts.Renderer, question string) string {
	return r.Render("grill_research", prompts.GrillResearchData{Idea: strings.TrimSpace(question)})
}

func grillIssueData(issueID, title, description string, files []attachfile.File) prompts.GrillIssueData {
	return prompts.GrillIssueData{
		ID:          issueID,
		Title:       strings.TrimSpace(title),
		Body:        grillIssueBody(description, files),
		Attachments: attachfile.Section(files),
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
