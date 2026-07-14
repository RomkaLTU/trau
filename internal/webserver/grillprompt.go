package webserver

import "strings"

// grillIssuePrompt is the first-turn prompt for grilling an existing issue: the
// agent interviews the user one question at a time via the ask_user MCP tool and
// ends with a finish_session proposal. It runs with the repo as cwd, so it is told
// to read the code before asking when that sharpens a question. Resume turns carry
// only the user's answer — the child already holds this context.
func grillIssuePrompt(issueID, title, description string) string {
	var b strings.Builder
	b.WriteString("You are clarifying a software issue so an autonomous coding agent can implement it without guessing. ")
	b.WriteString("You are running inside the repository this issue belongs to; read the code before asking when it sharpens a question.\n\n")
	b.WriteString("The issue under discussion:\n")
	b.WriteString(issueID)
	if t := strings.TrimSpace(title); t != "" {
		b.WriteString(" — ")
		b.WriteString(t)
	}
	b.WriteString("\n\n")
	if d := strings.TrimSpace(description); d != "" {
		b.WriteString(d)
	} else {
		b.WriteString("(no description yet)")
	}
	b.WriteString("\n\n")
	b.WriteString(grillPromptRules)
	return b.String()
}

// grillAuthoringPrompt is the first-turn prompt for a session anchored to the repo
// alone (no issue). Full from-scratch authoring lands in a later slice; here the
// agent still interviews toward a clear specification and proposes a rewrite.
func grillAuthoringPrompt() string {
	var b strings.Builder
	b.WriteString("You are helping the user turn a rough idea into a clear, implementable software issue for the repository ")
	b.WriteString("you are running inside; read the code before asking when it sharpens a question.\n\n")
	b.WriteString(grillPromptRules)
	return b.String()
}

const grillPromptRules = `How to run the session:
- Interview the user ONE question at a time, and only by calling the ask_user tool — never ask in plain assistant text. Wait for each answer before asking the next.
- Ask only what genuinely blocks a clean implementation: unclear scope, acceptance criteria, edge cases, affected files, dependencies. Skip anything you can settle yourself by reading the repository.
- If an ask_user call comes back saying the user has stepped away, stop immediately and end your turn with no further output. Do not ask again — the session resumes with their answer later.
- When the issue is clear enough, call finish_session with your proposed outcome:
  - "rewrite" — you can now write a complete, unambiguous issue description; pass it as proposed_description. This is the common case.
  - "split" — the work is epic-shaped: too big for one ticket but you can now slice it. Pass proposed_description as the parent rewrite framing the epic's goal, and sub_issues as the breakdown — one implementable slice per agent session, each with a title and a full description, and blocked_by listing the earlier sibling indices it depends on. The parent becomes the epic; the slices are created as its children.
  - "needs_split" — the work is too large for one ticket but you cannot confidently slice it yet; just flag it for a human to split.
  - "no_change" — the issue was already clear enough and needs no rewrite.
  Always include a short summary of the clarifications you reached. Nothing is written to the tracker until the user approves.`
