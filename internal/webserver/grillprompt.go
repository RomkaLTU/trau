package webserver

import (
	"strings"

	"github.com/RomkaLTU/trau/internal/attachfile"
)

// grillIssuePrompt is the first-turn prompt for grilling an existing issue: the
// agent interviews the user one question at a time via the ask_user MCP tool and
// ends with a finish_session proposal. It runs with the repo as cwd, so it is told
// to read the code before asking when that sharpens a question. Resume turns carry
// only the user's answer — the child already holds this context.
func grillIssuePrompt(issueID, title, description string, files []attachfile.File) string {
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
	b.WriteString(grillIssueBody(description, files))
	b.WriteString(grillPromptRules)
	return b.String()
}

// grillIssueBody renders the description with every reference to one of the
// issue's images repointed at the local copy the session materialized, followed by
// the list of those files — so the interviewing agent can open a screenshot the
// ticket only linked to.
func grillIssueBody(description string, files []attachfile.File) string {
	body := "(no description yet)"
	if d := strings.TrimSpace(description); d != "" {
		body = attachfile.Rewrite(d, files)
	}
	return body + "\n" + attachfile.Section(files) + "\n"
}

// grillPregrillPrompt is the first-turn prompt for an AFK pre-grill pass: no user
// is present, so the agent reads the repo and either finishes with a rewrite or
// no_change, or lodges its single opening question via ask_user — which parks at
// once — and ends its turn. The parked question waits for a live session later.
func grillPregrillPrompt(issueID, title, description string, files []attachfile.File) string {
	var b strings.Builder
	b.WriteString("You are triaging a software issue ahead of time so an autonomous coding agent can later implement it without guessing. ")
	b.WriteString("You are running inside the repository this issue belongs to; read the code before you decide.\n\n")
	b.WriteString("No user is available to answer right now — this is an unattended pass.\n\n")
	b.WriteString("The issue under discussion:\n")
	b.WriteString(issueID)
	if t := strings.TrimSpace(title); t != "" {
		b.WriteString(" — ")
		b.WriteString(t)
	}
	b.WriteString("\n\n")
	b.WriteString(grillIssueBody(description, files))
	b.WriteString(grillPregrillRules)
	return b.String()
}

// grillAuthoringPrompt is the first-turn prompt for a session anchored to the repo
// alone (no issue): the from-scratch authoring flow. The agent interviews the user
// toward a fully-specified new issue and ends with a create proposal — a single
// issue or an epic with sub-issues. idea is the one-line seed the user started with;
// it is empty when they opened the session without one.
func grillAuthoringPrompt(idea string) string {
	var b strings.Builder
	b.WriteString("You are helping the user turn a rough idea into a clear, implementable software issue for the repository ")
	b.WriteString("you are running inside; read the code before asking when it sharpens a question.\n\n")
	if i := strings.TrimSpace(idea); i != "" {
		b.WriteString("The idea to develop:\n")
		b.WriteString(i)
		b.WriteString("\n\n")
	} else {
		b.WriteString("The user has not written the idea down yet — open by asking what they want to build.\n\n")
	}
	b.WriteString(grillAuthoringRules)
	return b.String()
}

const grillPromptRules = `How to run the session:
- Interview the user ONE question at a time, and only by calling the ask_user tool — never ask in plain assistant text. Wait for each answer before asking the next.
- Whenever you offer options, mark the one you would choose with recommended (repeat that option's text exactly) and a one-line why — the user may not know the domain. Omit the recommendation only for pure-preference questions where no option is objectively better.
- Ask only what genuinely blocks a clean implementation: unclear scope, acceptance criteria, edge cases, affected files, dependencies. Skip anything you can settle yourself by reading the repository.
- If an ask_user call comes back saying the user has stepped away, stop immediately and end your turn with no further output. Do not ask again — the session resumes with their answer later.
- When the issue is clear enough, call finish_session with your proposed outcome:
  - "rewrite" — you can now write a complete, unambiguous issue description; pass it as proposed_description. This is the common case.
  - "split" — the work is epic-shaped: too big for one ticket but you can now slice it. Pass proposed_description as the parent rewrite framing the epic's goal, and sub_issues as the breakdown — one implementable slice per agent session, each with a title and a full description, and blocked_by listing the earlier sibling indices it depends on. The parent becomes the epic; the slices are created as its children.
  - "needs_split" — the work is too large for one ticket but you cannot confidently slice it yet; just flag it for a human to split.
  - "no_change" — the issue was already clear enough and needs no rewrite.
  Always include a short summary of the clarifications you reached. Nothing is written to the tracker until the user approves.`

const grillAuthoringRules = `How to run the session:
- Interview the user ONE question at a time, and only by calling the ask_user tool — never ask in plain assistant text. Wait for each answer before asking the next.
- Whenever you offer options, mark the one you would choose with recommended (repeat that option's text exactly) and a one-line why — the user may not know the domain. Omit the recommendation only for pure-preference questions where no option is objectively better.
- Ask only what genuinely blocks a clean specification: the goal, scope, acceptance criteria, edge cases, affected files, dependencies. Settle anything you can by reading the repository.
- If an ask_user call comes back saying the user has stepped away, stop immediately and end your turn with no further output. Do not ask again — the session resumes with their answer later.
- When you can specify the work fully, call finish_session with disposition "create":
  - For a single issue: pass a title and a proposed_description an agent can implement without guessing, and leave sub_issues empty. This is the common case.
  - For epic-shaped work: pass a title and proposed_description framing the epic's goal, and sub_issues as the breakdown — one implementable slice per agent session, each with a title and a full description, and blocked_by listing the earlier sibling indices it depends on. The epic is created as the parent; the slices become its children.
  - Use "no_change" only if the user decides not to file anything after all.
  Always include a short summary of what you specified. Nothing is created on the tracker until the user approves.`

const grillPregrillRules = `How to run this unattended pass:
- First read the repository to understand the issue as far as you can on your own; settle anything you can answer yourself instead of asking.
- If the issue is already clear enough to implement, call finish_session now:
  - "rewrite" — you can write a complete, unambiguous issue description; pass it as proposed_description. This is the common case when the issue only needed tightening.
  - "no_change" — the issue was already clear enough as written; say why in summary.
- Otherwise, ask the SINGLE most important opening question by calling ask_user exactly once. If you offer options with it, mark your recommended one (exact option text) and a one-line why. No user is present, so the call returns a park instruction: when it does, end your turn immediately without asking again. The question is saved and a live session resumes with the user's answer when they return.
- Ask your one opening question or finish — never both, and never call ask_user more than once or wait for an answer.
Always include a short summary of what you found. Nothing is written to the tracker until the user approves.`
